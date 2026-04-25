// Package server implements the gRPC service that fronts the SLTV
// daemon. It translates incoming protobuf messages into calls on the
// internal/disk service and reports node and cluster status.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	pb "github.com/sltv/sltv/api/proto/v1"
	"github.com/sltv/sltv/internal/cluster"
	"github.com/sltv/sltv/internal/disk"
	"github.com/sltv/sltv/internal/lvm"
	"github.com/sltv/sltv/internal/store"
	"github.com/sltv/sltv/internal/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server is the gRPC service. It is goroutine-safe.
type Server struct {
	pb.UnimplementedSltvServer

	disks   *disk.Service
	lvm     *lvm.Manager
	cluster *cluster.Manager // optional; nil in standalone mode
	nodeID  string
}

// Options configure a Server.
type Options struct {
	Disks   *disk.Service
	LVM     *lvm.Manager
	Cluster *cluster.Manager
	NodeID  string
}

// New constructs a Server.
func New(o Options) (*Server, error) {
	if o.Disks == nil || o.LVM == nil {
		return nil, errors.New("server: Disks and LVM are required")
	}
	if o.NodeID == "" {
		return nil, errors.New("server: NodeID is required")
	}
	return &Server{
		disks:   o.Disks,
		lvm:     o.LVM,
		cluster: o.Cluster,
		nodeID:  o.NodeID,
	}, nil
}

// Register binds the SLTV service onto a *grpc.Server.
func (s *Server) Register(g *grpc.Server) {
	pb.RegisterSltvServer(g, s)
}

// --- listeners -------------------------------------------------------------

// ListenerSpec describes a single listening endpoint.
type ListenerSpec struct {
	// Network is "unix" or "tcp".
	Network string
	// Address is the path or host:port.
	Address string
	// TLS, when non-nil, is applied to TCP listeners.
	TLS *tls.Config
}

// BuildListeners opens net.Listeners for each spec. Unix sockets have
// any stale file removed and the parent directory created with mode
// 0o755.
func BuildListeners(specs []ListenerSpec) ([]net.Listener, []credentials.TransportCredentials, error) {
	lns := make([]net.Listener, 0, len(specs))
	creds := make([]credentials.TransportCredentials, 0, len(specs))
	for _, sp := range specs {
		switch sp.Network {
		case "unix":
			if dir := filepath.Dir(sp.Address); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return nil, nil, fmt.Errorf("mkdir %s: %w", dir, err)
				}
			}
			_ = os.Remove(sp.Address)
			ln, err := net.Listen("unix", sp.Address)
			if err != nil {
				return nil, nil, fmt.Errorf("listen unix %s: %w", sp.Address, err)
			}
			if err := os.Chmod(sp.Address, 0o660); err != nil {
				_ = ln.Close()
				return nil, nil, fmt.Errorf("chmod %s: %w", sp.Address, err)
			}
			lns = append(lns, ln)
			creds = append(creds, nil)
		case "tcp":
			ln, err := net.Listen("tcp", sp.Address)
			if err != nil {
				return nil, nil, fmt.Errorf("listen tcp %s: %w", sp.Address, err)
			}
			lns = append(lns, ln)
			if sp.TLS != nil {
				creds = append(creds, credentials.NewTLS(sp.TLS))
			} else {
				creds = append(creds, nil)
			}
		default:
			return nil, nil, fmt.Errorf("server: unsupported network %q", sp.Network)
		}
	}
	return lns, creds, nil
}

// LoadServerTLS reads a cert/key pair and an optional client CA into a
// *tls.Config. When caPath is set, client auth is required (mTLS).
func LoadServerTLS(certPath, keyPath, caPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load cert/key: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if caPath != "" {
		caBytes, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, errors.New("ca file contained no PEM blocks")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// Serve runs grpc.Server on each listener until ctx is cancelled or
// any Serve() returns an error. GracefulStop is called on cancel.
func Serve(ctx context.Context, g *grpc.Server, listeners []net.Listener) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(listeners))
	for _, ln := range listeners {
		wg.Add(1)
		go func(ln net.Listener) {
			defer wg.Done()
			if err := g.Serve(ln); err != nil {
				errCh <- fmt.Errorf("serve %s: %w", ln.Addr(), err)
			}
		}(ln)
	}
	select {
	case <-ctx.Done():
		g.GracefulStop()
		wg.Wait()
		return ctx.Err()
	case err := <-errCh:
		g.GracefulStop()
		wg.Wait()
		return err
	}
}

// --- RPC implementations ---------------------------------------------------

// CreateDisk implements the gRPC method.
func (s *Server) CreateDisk(ctx context.Context, req *pb.CreateDiskRequest) (*pb.Disk, error) {
	rec, err := s.disks.CreateDisk(ctx, disk.CreateInput{
		Name:             req.GetName(),
		VG:               req.GetVg(),
		Mode:             modeFromProto(req.GetMode()),
		SizeBytes:        req.GetSizeBytes(),
		VirtualSizeBytes: req.GetVirtualSizeBytes(),
	})
	if err != nil {
		return nil, toGRPCError(err)
	}
	return diskToProto(rec), nil
}

// DeleteDisk implements the gRPC method.
func (s *Server) DeleteDisk(ctx context.Context, req *pb.DeleteDiskRequest) (*emptypb.Empty, error) {
	if err := s.disks.DeleteDisk(ctx, req.GetName(), req.GetForce()); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// GetDisk implements the gRPC method.
func (s *Server) GetDisk(ctx context.Context, req *pb.GetDiskRequest) (*pb.Disk, error) {
	rec, err := s.disks.GetDisk(ctx, req.GetName())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return diskToProto(rec), nil
}

// ListDisks implements the gRPC method.
func (s *Server) ListDisks(ctx context.Context, req *pb.ListDisksRequest) (*pb.ListDisksResponse, error) {
	recs, err := s.disks.ListDisks(ctx, req.GetVg())
	if err != nil {
		return nil, toGRPCError(err)
	}
	out := &pb.ListDisksResponse{Disks: make([]*pb.Disk, 0, len(recs))}
	for _, r := range recs {
		out.Disks = append(out.Disks, diskToProto(r))
	}
	return out, nil
}

// ExtendDisk implements the gRPC method.
func (s *Server) ExtendDisk(ctx context.Context, req *pb.ExtendDiskRequest) (*pb.Disk, error) {
	rec, err := s.disks.ExtendDisk(ctx, disk.ExtendInput{
		Name:       req.GetName(),
		SizeBytes:  req.GetSizeBytes(),
		DeltaBytes: req.GetDeltaBytes(),
	})
	if err != nil {
		return nil, toGRPCError(err)
	}
	return diskToProto(rec), nil
}

// AttachDisk implements the gRPC method.
func (s *Server) AttachDisk(ctx context.Context, req *pb.AttachDiskRequest) (*pb.Attachment, error) {
	a, err := s.disks.AttachDisk(ctx, disk.AttachInput{
		DiskName:  req.GetDiskName(),
		VMName:    req.GetVmName(),
		TargetDev: req.GetTargetDev(),
	})
	if err != nil {
		return nil, toGRPCError(err)
	}
	return attachmentToProto(a), nil
}

// DetachDisk implements the gRPC method.
func (s *Server) DetachDisk(ctx context.Context, req *pb.DetachDiskRequest) (*emptypb.Empty, error) {
	if err := s.disks.DetachDisk(ctx, req.GetDiskName(), req.GetVmName()); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// ListAttachments implements the gRPC method.
func (s *Server) ListAttachments(ctx context.Context, req *pb.ListAttachmentsRequest) (*pb.ListAttachmentsResponse, error) {
	atts, err := s.disks.ListAttachments(ctx, req.GetDiskName(), req.GetVmName())
	if err != nil {
		return nil, toGRPCError(err)
	}
	out := &pb.ListAttachmentsResponse{Attachments: make([]*pb.Attachment, 0, len(atts))}
	for _, a := range atts {
		out.Attachments = append(out.Attachments, attachmentToProto(a))
	}
	return out, nil
}

// GetNodeStatus implements the gRPC method.
func (s *Server) GetNodeStatus(ctx context.Context, _ *emptypb.Empty) (*pb.NodeStatus, error) {
	vgs, err := s.lvm.ListVGs(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	out := &pb.NodeStatus{
		NodeId:         s.nodeID,
		Version:        version.Version,
		ClusterEnabled: s.cluster != nil,
		Vgs:            make([]*pb.VolumeGroup, 0, len(vgs)),
	}
	for _, vg := range vgs {
		out.Vgs = append(out.Vgs, &pb.VolumeGroup{
			Name:      vg.Name,
			SizeBytes: vg.SizeBytes,
			FreeBytes: vg.FreeBytes,
		})
	}
	return out, nil
}

// GetClusterStatus implements the gRPC method.
func (s *Server) GetClusterStatus(ctx context.Context, _ *emptypb.Empty) (*pb.ClusterStatus, error) {
	out := &pb.ClusterStatus{Enabled: s.cluster != nil}
	disks, err := s.disks.ListDisks(ctx, "")
	if err != nil {
		return nil, toGRPCError(err)
	}
	atts, err := s.disks.ListAttachments(ctx, "", "")
	if err != nil {
		return nil, toGRPCError(err)
	}
	out.DiskCount = uint32(len(disks))
	out.AttachmentCount = uint32(len(atts))
	if s.cluster != nil {
		nodes, err := s.cluster.ListNodes(ctx)
		if err != nil {
			return nil, toGRPCError(err)
		}
		for _, n := range nodes {
			out.Nodes = append(out.Nodes, &pb.NodeStatus{NodeId: n.ID, Version: n.Endpoint, ClusterEnabled: true})
		}
		out.Leader = s.cluster.NodeID()
	} else {
		out.Nodes = []*pb.NodeStatus{{NodeId: s.nodeID, Version: version.Version}}
		out.Leader = s.nodeID
	}
	return out, nil
}

// --- conversion helpers ----------------------------------------------------

func modeFromProto(m pb.ProvisionMode) store.ProvisionMode {
	switch m {
	case pb.ProvisionMode_PROVISION_MODE_THICK:
		return store.ProvisionThick
	case pb.ProvisionMode_PROVISION_MODE_THIN:
		return store.ProvisionThin
	default:
		return store.ProvisionUnspecified
	}
}

func modeToProto(m store.ProvisionMode) pb.ProvisionMode {
	switch m {
	case store.ProvisionThick:
		return pb.ProvisionMode_PROVISION_MODE_THICK
	case store.ProvisionThin:
		return pb.ProvisionMode_PROVISION_MODE_THIN
	default:
		return pb.ProvisionMode_PROVISION_MODE_UNSPECIFIED
	}
}

func diskToProto(r store.DiskRecord) *pb.Disk {
	d := &pb.Disk{
		Name:             r.Name,
		Vg:               r.VG,
		Mode:             modeToProto(r.Mode),
		SizeBytes:        r.SizeBytes,
		VirtualSizeBytes: r.VirtualSizeBytes,
		DevicePath:       r.DevicePath,
		CreatedOnNode:    r.CreatedOnNode,
	}
	if !r.CreatedAt.IsZero() {
		d.CreatedAt = timestamppb.New(r.CreatedAt)
	}
	if !r.LastExtendedAt.IsZero() {
		d.LastExtendedAt = timestamppb.New(r.LastExtendedAt)
	}
	return d
}

func attachmentToProto(a store.AttachmentRecord) *pb.Attachment {
	out := &pb.Attachment{
		DiskName:  a.DiskName,
		VmName:    a.VMName,
		Host:      a.Host,
		TargetDev: a.TargetDev,
	}
	if !a.AttachedAt.IsZero() {
		out.AttachedAt = timestamppb.New(a.AttachedAt)
	}
	return out
}

// toGRPCError maps internal errors onto gRPC status codes so clients
// (and sctl in particular) can react to them with sensible exit codes.
func toGRPCError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, store.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	}
	if err == nil {
		return nil
	}
	return status.Error(codes.Internal, err.Error())
}
