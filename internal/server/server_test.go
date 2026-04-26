package server

import (
	"context"
	"net"
	"testing"
	"time"

	pb "github.com/sltv/sltv/api/proto/v1"
	"github.com/sltv/sltv/internal/cluster"
	"github.com/sltv/sltv/internal/disk"
	"github.com/sltv/sltv/internal/libvirtx"
	"github.com/sltv/sltv/internal/lvm"
	"github.com/sltv/sltv/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type fakeLVMRunner struct{}

func (fakeLVMRunner) Run(_ context.Context, name string, _ ...string) ([]byte, error) {
	if name == "vgs" {
		return []byte(`{"report":[{"vg":[{"vg_name":"vg-sltv","vg_size":"107374182400","vg_free":"96636764160"}]}]}`), nil
	}
	return nil, nil
}

func newServer(t *testing.T) (*grpc.ClientConn, func()) {
	t.Helper()
	lvmMgr := lvm.New(fakeLVMRunner{})
	libv := libvirtx.NewFakeClient("web01")
	st := store.NewMemoryStore()
	svc, err := disk.New(disk.Options{
		LVM: lvmMgr, Libvirt: libv, Store: st, Locker: cluster.NoopLocker{},
		QemuImg:            fakeQemuImg{},
		DefaultVG:          "vg-sltv",
		DefaultThinInitial: 10 << 30,
		NodeID:             "node-test",
		TempDir:            t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return frozen }

	srv, err := New(Options{Disks: svc, LVM: lvmMgr, NodeID: "node-test"})
	if err != nil {
		t.Fatal(err)
	}

	g := grpc.NewServer()
	srv.Register(g)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = g.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return conn, func() {
		_ = conn.Close()
		g.GracefulStop()
	}
}

type fakeQemuImg struct{}

func (fakeQemuImg) CreateQcow2(_ context.Context, _ string, _ uint64) error { return nil }
func (fakeQemuImg) DDOver(_ context.Context, _, _ string) error             { return nil }
func (fakeQemuImg) RemoveFile(_ string) error                               { return nil }

func TestServerCreateAndList(t *testing.T) {
	conn, stop := newServer(t)
	defer stop()
	c := pb.NewSltvClient(conn)

	d, err := c.CreateDisk(context.Background(), &pb.CreateDiskRequest{
		Name:      "data1",
		SizeBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("CreateDisk: %v", err)
	}
	if d.Mode != pb.ProvisionMode_PROVISION_MODE_THICK {
		t.Errorf("mode = %v", d.Mode)
	}

	_, err = c.CreateDisk(context.Background(), &pb.CreateDiskRequest{Name: "data1", SizeBytes: 1 << 30})
	if status.Code(err).String() != "AlreadyExists" {
		t.Errorf("expected AlreadyExists, got %v", err)
	}

	resp, err := c.ListDisks(context.Background(), &pb.ListDisksRequest{})
	if err != nil || len(resp.Disks) != 1 {
		t.Errorf("ListDisks = %v %v", resp, err)
	}
}

func TestServerNodeStatus(t *testing.T) {
	conn, stop := newServer(t)
	defer stop()
	c := pb.NewSltvClient(conn)
	st, err := c.GetNodeStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetNodeStatus: %v", err)
	}
	if st.NodeId != "node-test" {
		t.Errorf("node id = %s", st.NodeId)
	}
	if len(st.Vgs) != 1 || st.Vgs[0].Name != "vg-sltv" {
		t.Errorf("vgs = %v", st.Vgs)
	}
}

func TestServerNotFound(t *testing.T) {
	conn, stop := newServer(t)
	defer stop()
	c := pb.NewSltvClient(conn)
	_, err := c.GetDisk(context.Background(), &pb.GetDiskRequest{Name: "ghost"})
	if status.Code(err).String() != "NotFound" {
		t.Errorf("expected NotFound, got %v", err)
	}
}
