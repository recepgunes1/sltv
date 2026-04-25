package cluster

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNoopLocker(t *testing.T) {
	var l Locker = NoopLocker{}
	rel, err := l.Acquire(context.Background(), "vg-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rel()
}

// fakeLocker is used by higher-layer tests (disk.Service) to verify
// that the lock contract is honoured, and also by this package as a
// simple smoke test of the interface.
type fakeLocker struct {
	calls atomic.Int64
}

func (f *fakeLocker) Acquire(_ context.Context, _ string) (func(), error) {
	f.calls.Add(1)
	return func() {}, nil
}

func TestFakeLockerCounts(t *testing.T) {
	f := &fakeLocker{}
	for i := 0; i < 5; i++ {
		rel, err := f.Acquire(context.Background(), "vg")
		if err != nil {
			t.Fatal(err)
		}
		rel()
	}
	if f.calls.Load() != 5 {
		t.Errorf("calls = %d", f.calls.Load())
	}
}

func TestNewManagerNoEndpoints(t *testing.T) {
	_, err := NewManager(context.Background(), Options{NodeID: "x"})
	if err == nil {
		t.Errorf("expected error for empty endpoints")
	}
}

func TestNewManagerNoNodeID(t *testing.T) {
	_, err := NewManager(context.Background(), Options{Endpoints: []string{"http://127.0.0.1:0"}})
	if err == nil {
		t.Errorf("expected error for empty node id")
	}
}

// TestManagerEndToEnd is an integration test gated on the presence of
// a real etcd. It is enabled only when SLTV_TEST_ETCD is set to a
// comma-separated list of endpoints.
func TestManagerEndToEnd(t *testing.T) {
	endpoints := splitEndpoints(os.Getenv("SLTV_TEST_ETCD"))
	if len(endpoints) == 0 {
		t.Skip("set SLTV_TEST_ETCD=http://127.0.0.1:2379 to enable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	m, err := NewManager(ctx, Options{
		Endpoints: endpoints,
		NodeID:    "node-test",
		Prefix:    "/sltv-test",
		LockTTL:   5 * time.Second,
		Version:   "test",
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	rel, err := m.Locker().Acquire(ctx, "vg-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rel()

	nodes, err := m.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == "node-test" {
			found = true
		}
	}
	if !found {
		t.Errorf("node not found in ListNodes: %+v", nodes)
	}
}

func splitEndpoints(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
