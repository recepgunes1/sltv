// Command sltvd is the SLTV daemon. It exposes a gRPC API on a Unix
// socket (and optionally a TCP/TLS port) and runs the thin-provision
// auto-extend manager in the background.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/sltv/sltv/internal/cluster"
	"github.com/sltv/sltv/internal/config"
	"github.com/sltv/sltv/internal/disk"
	"github.com/sltv/sltv/internal/libvirtx"
	"github.com/sltv/sltv/internal/lvm"
	"github.com/sltv/sltv/internal/server"
	"github.com/sltv/sltv/internal/store"
	"github.com/sltv/sltv/internal/thin"
	"github.com/sltv/sltv/internal/version"
)

// fatal logs and exits with the given code. We do not call slog.Error
// from this function because the slog handler is not yet configured
// in the early bootstrap path.
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sltvd: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	var (
		configPath  = flag.String("config", config.DefaultPath, "path to sltvd.yaml")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		if errors.Is(err, config.ErrFileNotFound) {
			fmt.Fprintf(os.Stderr, "sltvd: config not found at %s; using defaults+env\n", *configPath)
			cfg, err = config.Load("")
		}
		if err != nil {
			fatal("load config: %v", err)
		}
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)
	logger.Info("sltvd starting", "version", version.String(), "node_id", cfg.EffectiveNodeID())

	if err := run(cfg, logger); err != nil {
		logger.Error("sltvd exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("sltvd shutdown complete")
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func run(cfg config.Config, logger *slog.Logger) error {
	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := os.MkdirAll(cfg.Storage.StateDir, 0o755); err != nil { //nolint:gosec
		return fmt.Errorf("mkdir state dir: %w", err)
	}

	var (
		st           store.Store
		clusterMgr   *cluster.Manager
		locker       cluster.Locker = cluster.NoopLocker{}
		closeStore                  = func() {}
		closeCluster                = func() {}
	)

	if cfg.Cluster.Enabled {
		mgr, err := cluster.NewManager(rootCtx, cluster.Options{
			Endpoints:   cfg.Cluster.ETCD.Endpoints,
			NodeID:      cfg.EffectiveNodeID(),
			Prefix:      "/sltv",
			LockTTL:     cfg.Cluster.ETCD.LockTTL,
			DialTimeout: 5 * time.Second,
			TLS: cluster.TLSConfig{
				Cert: cfg.Cluster.ETCD.TLS.Cert,
				Key:  cfg.Cluster.ETCD.TLS.Key,
				CA:   cfg.Cluster.ETCD.TLS.CA,
			},
			Version: version.Version,
		})
		if err != nil {
			return fmt.Errorf("init cluster: %w", err)
		}
		clusterMgr = mgr
		locker = mgr.Locker()
		st = store.NewEtcdStore(mgr.Client(), "/sltv")
		closeCluster = func() { _ = mgr.Close() }
		logger.Info("cluster mode enabled", "endpoints", cfg.Cluster.ETCD.Endpoints)
	} else {
		bolt, err := store.OpenBoltStore(filepath.Join(cfg.Storage.StateDir, "state.db"))
		if err != nil {
			return fmt.Errorf("open bolt store: %w", err)
		}
		st = bolt
		closeStore = func() { _ = bolt.Close() }
		logger.Info("standalone mode", "state_db", filepath.Join(cfg.Storage.StateDir, "state.db"))
	}
	defer closeStore()
	defer closeCluster()

	lvmMgr := lvm.New(lvm.ExecRunner{})
	libvClient := libvirtx.NewVirshClient(cfg.Libvirt.URI, libvirtx.ExecRunner{})

	diskSvc, err := disk.New(disk.Options{
		LVM:                lvmMgr,
		Libvirt:            libvClient,
		Store:              st,
		Locker:             locker,
		QemuImg:            disk.ExecQemuImgRunner{},
		DefaultVG:          cfg.Storage.DefaultVG,
		DefaultThinInitial: cfg.Thin.DefaultInitialSize.Bytes(),
		NodeID:             cfg.EffectiveNodeID(),
		TempDir:            filepath.Join(cfg.Storage.StateDir, "tmp"),
	})
	if err != nil {
		return fmt.Errorf("init disk service: %w", err)
	}

	thinMgr, err := thin.New(thin.Options{
		Disks:                diskSvc,
		Libvirt:              libvClient,
		Store:                st,
		Logger:               logger.With("component", "thin"),
		PollInterval:         cfg.Thin.PollInterval,
		FreeThresholdPercent: cfg.Thin.FreeThresholdPercent,
		ExtendStepPercent:    cfg.Thin.ExtendStepPercent,
		NodeID:               cfg.EffectiveNodeID(),
	})
	if err != nil {
		return fmt.Errorf("init thin manager: %w", err)
	}
	go thinMgr.Run(rootCtx)
	defer thinMgr.Stop()

	srv, err := server.New(server.Options{
		Disks:   diskSvc,
		LVM:     lvmMgr,
		Cluster: clusterMgr,
		NodeID:  cfg.EffectiveNodeID(),
	})
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}

	specs := []server.ListenerSpec{}
	if cfg.Listen.Unix != "" {
		specs = append(specs, server.ListenerSpec{Network: "unix", Address: cfg.Listen.Unix})
	}
	if cfg.Listen.TCP != "" {
		tlsCfg, err := server.LoadServerTLS(cfg.TLS.Cert, cfg.TLS.Key, cfg.TLS.CA)
		if err != nil {
			return fmt.Errorf("load tls: %w", err)
		}
		specs = append(specs, server.ListenerSpec{Network: "tcp", Address: cfg.Listen.TCP, TLS: tlsCfg})
	}
	listeners, creds, err := server.BuildListeners(specs)
	if err != nil {
		return fmt.Errorf("build listeners: %w", err)
	}
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	gservers := make([]*grpc.Server, len(listeners))
	for i, c := range creds {
		var opts []grpc.ServerOption
		if c != nil {
			opts = append(opts, grpc.Creds(c))
		}
		g := grpc.NewServer(opts...)
		srv.Register(g)
		gservers[i] = g
	}

	errCh := make(chan error, len(listeners))
	for i, ln := range listeners {
		i, ln := i, ln
		go func() {
			logger.Info("grpc serving", "addr", ln.Addr())
			if err := gservers[i].Serve(ln); err != nil {
				errCh <- err
			}
		}()
	}

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		logger.Error("listener failed", "err", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	for _, g := range gservers {
		go g.GracefulStop()
	}
	<-stopCtx.Done()
	return nil
}

// avoid an unused import warning when building without listener types.
var _ = net.Listen
