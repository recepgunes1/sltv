package disk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sltv/sltv/internal/cluster"
	"github.com/sltv/sltv/internal/libvirtx"
	"github.com/sltv/sltv/internal/lvm"
	"github.com/sltv/sltv/internal/store"
)

// fakeLVMRunner records every command the lvm wrapper would invoke
// and lets tests assert on the sequence.
type fakeLVMRunner struct {
	cmds [][]string
}

func (f *fakeLVMRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{name}, args...))
	return nil, nil
}

// fakeQemuImg records its calls, never touches disk.
type fakeQemuImg struct {
	created []string
	dds     [][2]string
	removed []string
}

func (f *fakeQemuImg) CreateQcow2(_ context.Context, p string, _ uint64) error {
	f.created = append(f.created, p)
	return nil
}
func (f *fakeQemuImg) DDOver(_ context.Context, src, dst string) error {
	f.dds = append(f.dds, [2]string{src, dst})
	return nil
}
func (f *fakeQemuImg) RemoveFile(p string) error {
	f.removed = append(f.removed, p)
	return nil
}

func newTestService(t *testing.T) (*Service, *fakeLVMRunner, *libvirtx.FakeClient, *fakeQemuImg, *store.MemoryStore) {
	t.Helper()
	lvmRunner := &fakeLVMRunner{}
	mgr := lvm.New(lvmRunner)
	libv := libvirtx.NewFakeClient()
	st := store.NewMemoryStore()
	qi := &fakeQemuImg{}
	svc, err := New(Options{
		LVM:                mgr,
		Libvirt:            libv,
		Store:              st,
		Locker:             cluster.NoopLocker{},
		QemuImg:            qi,
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
	return svc, lvmRunner, libv, qi, st
}

func TestCreateThick(t *testing.T) {
	svc, lvmR, _, _, st := newTestService(t)
	rec, err := svc.CreateDisk(context.Background(), CreateInput{
		Name:      "data1",
		SizeBytes: 50 * 1024 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("CreateDisk: %v", err)
	}
	if rec.Mode != store.ProvisionThick || rec.VG != "vg-sltv" || rec.DevicePath != "/dev/vg-sltv/data1" {
		t.Errorf("rec = %+v", rec)
	}
	got, err := st.GetDisk(context.Background(), "data1")
	if err != nil || got.Name != "data1" {
		t.Errorf("store: %+v err=%v", got, err)
	}
	if len(lvmR.cmds) != 1 || lvmR.cmds[0][0] != "lvcreate" {
		t.Errorf("expected lvcreate; got %v", lvmR.cmds)
	}
}

func TestCreateThin(t *testing.T) {
	svc, _, _, qi, st := newTestService(t)
	rec, err := svc.CreateDisk(context.Background(), CreateInput{
		Name:             "thin1",
		Mode:             store.ProvisionThin,
		VirtualSizeBytes: 100 * 1024 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("CreateDisk: %v", err)
	}
	if rec.Mode != store.ProvisionThin || rec.SizeBytes != 10*1024*1024*1024 || rec.VirtualSizeBytes != 100*1024*1024*1024 {
		t.Errorf("rec = %+v", rec)
	}
	if len(qi.created) != 1 || len(qi.dds) != 1 || len(qi.removed) != 1 {
		t.Errorf("qemu-img usage: created=%v dds=%v removed=%v", qi.created, qi.dds, qi.removed)
	}
	if qi.dds[0][1] != "/dev/vg-sltv/thin1" {
		t.Errorf("dd target = %s", qi.dds[0][1])
	}
	got, _ := st.GetDisk(context.Background(), "thin1")
	if got.Mode != store.ProvisionThin {
		t.Errorf("store mode = %v", got.Mode)
	}
}

func TestCreateValidation(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	cases := []struct {
		name string
		in   CreateInput
		want string
	}{
		{"missing name", CreateInput{SizeBytes: 1}, "name is required"},
		{"bad name", CreateInput{Name: "bad name"}, "invalid name"},
		{"thin missing virtual", CreateInput{Name: "x", Mode: store.ProvisionThin}, "virtual_size_bytes"},
		{"thin >= virtual", CreateInput{Name: "x", Mode: store.ProvisionThin, SizeBytes: 100, VirtualSizeBytes: 100}, "must be <"},
		{"thick zero size", CreateInput{Name: "x"}, "size_bytes is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateDisk(context.Background(), tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want err containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCreateDuplicate(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	_, err := svc.CreateDisk(context.Background(), CreateInput{Name: "dup", SizeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateDisk(context.Background(), CreateInput{Name: "dup", SizeBytes: 1024})
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestAttachDetach(t *testing.T) {
	svc, _, libv, _, _ := newTestService(t)
	libv.Domains["web01"] = struct{}{}
	libv.Attached["web01"] = map[string]libvirtx.DiskAttachSpec{}
	if _, err := svc.CreateDisk(context.Background(), CreateInput{Name: "data1", SizeBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	att, err := svc.AttachDisk(context.Background(), AttachInput{DiskName: "data1", VMName: "web01"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if att.TargetDev != "vdb" {
		t.Errorf("target dev = %s, want vdb", att.TargetDev)
	}
	if _, ok := libv.Attached["web01"]["vdb"]; !ok {
		t.Errorf("libvirt fake missing attachment: %+v", libv.Attached)
	}
	atts, _ := svc.ListAttachments(context.Background(), "data1", "")
	if len(atts) != 1 {
		t.Errorf("list atts = %d", len(atts))
	}

	if err := svc.DetachDisk(context.Background(), "data1", "web01"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if _, ok := libv.Attached["web01"]["vdb"]; ok {
		t.Errorf("expected detach to remove attachment")
	}
}

func TestAttachUnknownVM(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	if _, err := svc.CreateDisk(context.Background(), CreateInput{Name: "d", SizeBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AttachDisk(context.Background(), AttachInput{DiskName: "d", VMName: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected domain not found, got %v", err)
	}
}

func TestExtendAbsolute(t *testing.T) {
	svc, lvmR, libv, _, _ := newTestService(t)
	libv.Domains["web01"] = struct{}{}
	libv.Attached["web01"] = map[string]libvirtx.DiskAttachSpec{}
	if _, err := svc.CreateDisk(context.Background(), CreateInput{Name: "x", SizeBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachDisk(context.Background(), AttachInput{DiskName: "x", VMName: "web01"}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ExtendDisk(context.Background(), ExtendInput{Name: "x", SizeBytes: 4096})
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if got.SizeBytes != 4096 {
		t.Errorf("size = %d", got.SizeBytes)
	}
	// last command should be lvextend with absolute size
	last := lvmR.cmds[len(lvmR.cmds)-1]
	if last[0] != "lvextend" {
		t.Errorf("last cmd = %v", last)
	}
	if len(libv.Resizes) != 1 || libv.Resizes[0].SizeBytes != 4096 {
		t.Errorf("resizes = %v", libv.Resizes)
	}
}

func TestExtendDelta(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	if _, err := svc.CreateDisk(context.Background(), CreateInput{Name: "x", SizeBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ExtendDisk(context.Background(), ExtendInput{Name: "x", DeltaBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if got.SizeBytes != 2048 {
		t.Errorf("delta resulted in size=%d", got.SizeBytes)
	}
}

func TestExtendInputValidation(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	if _, err := svc.CreateDisk(context.Background(), CreateInput{Name: "x", SizeBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExtendDisk(context.Background(), ExtendInput{Name: "x"}); err == nil {
		t.Errorf("expected error for both zero")
	}
	if _, err := svc.ExtendDisk(context.Background(), ExtendInput{Name: "x", SizeBytes: 1, DeltaBytes: 1}); err == nil {
		t.Errorf("expected error for both set")
	}
}

func TestDeleteRequiresForceWhenAttached(t *testing.T) {
	svc, _, libv, _, _ := newTestService(t)
	libv.Domains["web01"] = struct{}{}
	libv.Attached["web01"] = map[string]libvirtx.DiskAttachSpec{}
	if _, err := svc.CreateDisk(context.Background(), CreateInput{Name: "d", SizeBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachDisk(context.Background(), AttachInput{DiskName: "d", VMName: "web01"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteDisk(context.Background(), "d", false); err == nil {
		t.Errorf("expected error: attached without force")
	}
	if err := svc.DeleteDisk(context.Background(), "d", true); err != nil {
		t.Fatalf("force delete: %v", err)
	}
}

func TestExtendByPercent(t *testing.T) {
	svc, lvmR, _, _, _ := newTestService(t)
	if _, err := svc.CreateDisk(context.Background(), CreateInput{Name: "x", SizeBytes: 1000}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ExtendByPercent(context.Background(), "x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.SizeBytes != 1100 {
		t.Errorf("size = %d, want 1100", got.SizeBytes)
	}
	last := lvmR.cmds[len(lvmR.cmds)-1]
	if last[0] != "lvextend" || last[1] != "-l" || last[2] != "+10%LV" {
		t.Errorf("last cmd = %v", last)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Errorf("expected error for empty options")
	}
}
