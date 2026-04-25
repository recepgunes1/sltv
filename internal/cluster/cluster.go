// Package cluster wraps the optional ETCD-backed coordination layer
// used by sltvd in cluster mode. Its responsibilities are:
//
//   - Maintain a node membership lease under /<prefix>/nodes/<id> so
//     peers can discover each other.
//   - Provide a Locker that serialises LVM metadata operations on a
//     given Volume Group across all hosts that share it.
//   - Expose a *clientv3.Client for use by store.EtcdStore.
//
// When clustering is disabled the package's NoopLocker is used so call
// sites do not need to special-case standalone mode.
package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// Locker provides per-VG mutual exclusion across nodes.
//
// Acquire blocks until the lock is held or ctx is cancelled. The
// returned release function is idempotent and must be called by the
// caller (typically via defer) to free the lock.
type Locker interface {
	Acquire(ctx context.Context, vg string) (release func(), err error)
}

// NoopLocker satisfies Locker without any blocking. It is the default
// when clustering is disabled.
type NoopLocker struct{}

// Acquire implements Locker.
func (NoopLocker) Acquire(_ context.Context, _ string) (func(), error) {
	return func() {}, nil
}

// Options configure a Manager.
type Options struct {
	Endpoints []string
	NodeID    string
	// Prefix is the ETCD key prefix sltvd owns. Default: "/sltv".
	Prefix string
	// LockTTL is the etcd session TTL backing per-VG locks. Lock
	// holders that crash will release after at most LockTTL.
	LockTTL time.Duration
	// DialTimeout, when non-zero, bounds the initial connection.
	DialTimeout time.Duration
	// TLS configures mTLS for the etcd client. Optional.
	TLS TLSConfig
	// Version of sltvd; reported in the membership record.
	Version string
}

// TLSConfig holds paths to PEM credentials.
type TLSConfig struct {
	Cert string
	Key  string
	CA   string
}

// Manager owns the etcd client and the membership goroutine.
type Manager struct {
	cli     *clientv3.Client
	session *concurrency.Session
	prefix  string
	nodeID  string

	stopOnce sync.Once
	cancel   context.CancelFunc
	doneCh   chan struct{}
}

// NewManager dials etcd, creates a session for locks, registers this
// node in the membership map, and starts a heartbeat keep-alive
// goroutine. The caller must call Close to deregister.
func NewManager(ctx context.Context, opts Options) (*Manager, error) {
	if len(opts.Endpoints) == 0 {
		return nil, errors.New("cluster: at least one etcd endpoint is required")
	}
	if opts.NodeID == "" {
		return nil, errors.New("cluster: node id is required")
	}
	if opts.Prefix == "" {
		opts.Prefix = "/sltv"
	}
	if opts.LockTTL <= 0 {
		opts.LockTTL = 10 * time.Second
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 5 * time.Second
	}

	tlsConf, err := buildTLSConfig(opts.TLS)
	if err != nil {
		return nil, err
	}
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   opts.Endpoints,
		DialTimeout: opts.DialTimeout,
		TLS:         tlsConf,
		Context:     ctx,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: etcd dial: %w", err)
	}
	sess, err := concurrency.NewSession(cli, concurrency.WithTTL(int(opts.LockTTL.Seconds())))
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("cluster: session: %w", err)
	}
	bgCtx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cli:     cli,
		session: sess,
		prefix:  strings.TrimRight(opts.Prefix, "/"),
		nodeID:  opts.NodeID,
		cancel:  cancel,
		doneCh:  make(chan struct{}),
	}
	if err := m.registerNode(ctx, opts.Version); err != nil {
		_ = sess.Close()
		_ = cli.Close()
		cancel()
		return nil, err
	}
	go m.heartbeat(bgCtx, opts.Version)
	return m, nil
}

// Client returns the underlying etcd client; callers must not Close it.
func (m *Manager) Client() *clientv3.Client { return m.cli }

// Prefix returns the base prefix the manager owns (e.g. "/sltv").
func (m *Manager) Prefix() string { return m.prefix }

// Locker returns a Locker that uses the manager's etcd session for the
// underlying mutexes.
func (m *Manager) Locker() Locker { return &etcdLocker{session: m.session, prefix: m.prefix} }

// NodeID returns this manager's stable node id.
func (m *Manager) NodeID() string { return m.nodeID }

// Close releases the etcd session and client. Idempotent.
func (m *Manager) Close() error {
	m.stopOnce.Do(func() {
		m.cancel()
		<-m.doneCh
		_ = m.session.Close()
		_ = m.cli.Close()
	})
	return nil
}

// ListNodes returns the registered peers' encoded NodeInfo.
func (m *Manager) ListNodes(ctx context.Context) ([]NodeInfo, error) {
	resp, err := m.cli.Get(ctx, m.nodesPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]NodeInfo, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		out = append(out, NodeInfo{
			ID:       path.Base(string(kv.Key)),
			Endpoint: string(kv.Value),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// NodeInfo is a sltvd peer record.
type NodeInfo struct {
	ID       string
	Endpoint string
}

func (m *Manager) nodesPrefix() string {
	return m.prefix + "/nodes/"
}

func (m *Manager) nodeKey() string {
	return m.nodesPrefix() + m.nodeID
}

func (m *Manager) registerNode(ctx context.Context, version string) error {
	host, _ := os.Hostname()
	val := fmt.Sprintf("%s|%s", host, version)
	_, err := m.cli.Put(ctx, m.nodeKey(), val, clientv3.WithLease(m.session.Lease()))
	if err != nil {
		return fmt.Errorf("cluster: register node: %w", err)
	}
	return nil
}

// heartbeat re-registers the node periodically to refresh the lease in
// the unlikely event the session lease keep-alive cannot keep up. The
// concurrency.Session already does keep-alive internally; this loop
// also re-puts the value so reflected changes (version bumps) appear.
func (m *Manager) heartbeat(ctx context.Context, version string) {
	defer close(m.doneCh)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = m.registerNode(ctx, version)
		}
	}
}

// --- Locker implementation -------------------------------------------------

type etcdLocker struct {
	session *concurrency.Session
	prefix  string
}

func (l *etcdLocker) Acquire(ctx context.Context, vg string) (func(), error) {
	if vg == "" {
		return nil, errors.New("cluster: vg required")
	}
	mtx := concurrency.NewMutex(l.session, l.prefix+"/locks/vg/"+vg)
	if err := mtx.Lock(ctx); err != nil {
		return nil, fmt.Errorf("cluster: lock %s: %w", vg, err)
	}
	released := false
	var mu sync.Mutex
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if released {
			return
		}
		released = true
		// Use a short timeout so we don't block forever during shutdown.
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mtx.Unlock(c)
	}, nil
}

// --- TLS -------------------------------------------------------------------

func buildTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	if cfg.Cert == "" && cfg.Key == "" && cfg.CA == "" {
		return nil, nil
	}
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.Cert != "" || cfg.Key != "" {
		cert, err := tls.LoadX509KeyPair(cfg.Cert, cfg.Key)
		if err != nil {
			return nil, fmt.Errorf("cluster: load tls keypair: %w", err)
		}
		tlsConf.Certificates = []tls.Certificate{cert}
	}
	if cfg.CA != "" {
		caBytes, err := os.ReadFile(cfg.CA)
		if err != nil {
			return nil, fmt.Errorf("cluster: read ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, errors.New("cluster: no PEM blocks in ca file")
		}
		tlsConf.RootCAs = pool
	}
	return tlsConf, nil
}
