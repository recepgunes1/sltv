package thin

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sltv/sltv/internal/cluster"
	"github.com/sltv/sltv/internal/disk"
	"github.com/sltv/sltv/internal/libvirtx"
	"github.com/sltv/sltv/internal/lvm"
	"github.com/sltv/sltv/internal/store"
)

type fakeLVMRunner struct{}

func (fakeLVMRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, nil
}

type fakeQemuImg struct{}

func (fakeQemuImg) CreateQcow2(_ context.Context, _ string, _ uint64) error { return nil }
func (fakeQemuImg) DDOver(_ context.Context, _, _ string) error             { return nil }
func (fakeQemuImg) RemoveFile(_ string) error                                { return nil }

func newServiceFixture(t *testing.T, vm string) (*disk.Service, *libvirtx.FakeClient, store.Store) {
	t.Helper()
	libv := libvirtx.NewFakeClient(vm)
	st := store.NewMemoryStore()
	svc, err := disk.New(disk.Options{
		LVM:                lvm.New(fakeLVMRunner{}),
		Libvirt:            libv,
		Store:              st,
		Locker:             cluster.NoopLocker{},
		QemuImg:            fakeQemuImg{},
		DefaultVG:          "vg-sltv",
		DefaultThinInitial: 10 * 1024 * 1024 * 1024,
		NodeID:             "node-test",
		TempDir:            t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return frozen }
	return svc, libv, st
}

func TestTickNoOpWhenAboveThreshold(t *testing.T) {
	svc, libv, st := newServiceFixture(t, "web01")
	if _, err := svc.CreateDisk(context.Background(), disk.CreateInput{
		Name: "thin1", Mode: store.ProvisionThin, VirtualSizeBytes: 100 * 1024 * 1024 * 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachDisk(context.Background(), disk.AttachInput{DiskName: "thin1", VMName: "web01"}); err != nil {
		t.Fatal(err)
	}
	libv.Stats["web01"] = []libvirtx.BlockStats{{
		Index: 0, Name: "vdb", PhysicalBytes: 10 * 1024 * 1024 * 1024,
		AllocationBytes: 1 * 1024 * 1024 * 1024,
	}}

	mgr, err := New(Options{
		Disks: svc, Libvirt: libv, Store: st,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval:         time.Hour,
		FreeThresholdPercent: 10,
		ExtendStepPercent:    10,
		NodeID:               "node-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.Tick(context.Background())

	got, _ := st.GetDisk(context.Background(), "thin1")
	if got.SizeBytes != 10*1024*1024*1024 {
		t.Errorf("disk should not have grown, size=%d", got.SizeBytes)
	}
	if !got.LastExtendedAt.IsZero() {
		t.Errorf("LastExtendedAt should be zero")
	}
}

func TestTickExtendsWhenBelowThreshold(t *testing.T) {
	svc, libv, st := newServiceFixture(t, "web01")
	if _, err := svc.CreateDisk(context.Background(), disk.CreateInput{
		Name: "thin1", Mode: store.ProvisionThin, VirtualSizeBytes: 100 * 1024 * 1024 * 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachDisk(context.Background(), disk.AttachInput{DiskName: "thin1", VMName: "web01"}); err != nil {
		t.Fatal(err)
	}
	// 10G physical, 9.5G allocated -> 5% free, below 10% threshold.
	libv.Stats["web01"] = []libvirtx.BlockStats{{
		Index: 0, Name: "vdb", PhysicalBytes: 10 * 1024 * 1024 * 1024,
		AllocationBytes: uint64(9.5 * float64(1024*1024*1024)),
	}}

	mgr, err := New(Options{
		Disks: svc, Libvirt: libv, Store: st,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval:         time.Hour,
		FreeThresholdPercent: 10,
		ExtendStepPercent:    10,
		NodeID:               "node-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.Tick(context.Background())

	got, _ := st.GetDisk(context.Background(), "thin1")
	want := uint64(10*1024*1024*1024) + (10*1024*1024*1024)/10
	if got.SizeBytes != want {
		t.Errorf("expected size %d, got %d", want, got.SizeBytes)
	}
	if got.LastExtendedAt.IsZero() {
		t.Errorf("LastExtendedAt should be updated")
	}
	if len(libv.Resizes) != 1 || libv.Resizes[0].VM != "web01" || libv.Resizes[0].TargetDev != "vdb" {
		t.Errorf("expected one BlockResize, got %+v", libv.Resizes)
	}
}

func TestTickIgnoresOtherHosts(t *testing.T) {
	svc, libv, st := newServiceFixture(t, "web01")
	if _, err := svc.CreateDisk(context.Background(), disk.CreateInput{
		Name: "thin1", Mode: store.ProvisionThin, VirtualSizeBytes: 100 * 1024 * 1024 * 1024,
	}); err != nil {
		t.Fatal(err)
	}
	// Manually insert an attachment on a different host.
	if err := st.CreateAttachment(context.Background(), store.AttachmentRecord{
		DiskName: "thin1", VMName: "web02", Host: "other-node", TargetDev: "vdb",
	}); err != nil {
		t.Fatal(err)
	}

	mgr, err := New(Options{
		Disks: svc, Libvirt: libv, Store: st,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval:         time.Hour,
		FreeThresholdPercent: 10,
		ExtendStepPercent:    10,
		NodeID:               "node-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Should not panic / error out, and should not touch domains it
	// does not own.
	mgr.Tick(context.Background())
	if len(libv.Resizes) != 0 {
		t.Errorf("did not expect any resizes, got %v", libv.Resizes)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Errorf("expected error on empty options")
	}
}
