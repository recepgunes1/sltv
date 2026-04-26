// Package disk implements the SLTV disk lifecycle: create/delete,
// attach/detach, extend, and list. It is the heart of the daemon and
// orchestrates lvm2, libvirt, the persistent store, and the cluster
// lock together.
//
// All write operations on a Volume Group are serialised by the
// configured Locker (a NoopLocker in standalone mode, an
// ETCD-backed concurrency.Mutex in cluster mode). The store is the
// source of truth; lvm2 and libvirt are second-system effects.
package disk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sltv/sltv/internal/cluster"
	"github.com/sltv/sltv/internal/libvirtx"
	"github.com/sltv/sltv/internal/lvm"
	"github.com/sltv/sltv/internal/store"
)

// QemuImgRunner shells out to qemu-img and dd; abstracted to allow
// tests to substitute a fake.
type QemuImgRunner interface {
	// CreateQcow2 creates a qcow2 image at imagePath of the given
	// virtual size. Equivalent to `qemu-img create -f qcow2 <path> <size>`.
	CreateQcow2(ctx context.Context, imagePath string, virtualSizeBytes uint64) error
	// DDOver copies src bytes into dst (block device), without
	// truncating dst. Equivalent to `dd if=<src> of=<dst> bs=1M conv=notrunc`.
	DDOver(ctx context.Context, src, dst string) error
	// RemoveFile removes a file (used to clean up temporary qcow2
	// header files).
	RemoveFile(path string) error
}

// ExecQemuImgRunner is the default real implementation.
type ExecQemuImgRunner struct{}

// CreateQcow2 implements QemuImgRunner.
func (ExecQemuImgRunner) CreateQcow2(ctx context.Context, imagePath string, virtualSizeBytes uint64) error {
	cmd := exec.CommandContext(ctx, "qemu-img", "create", "-f", "qcow2", imagePath, fmt.Sprintf("%d", virtualSizeBytes)) //nolint:gosec
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu-img create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DDOver implements QemuImgRunner.
func (ExecQemuImgRunner) DDOver(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "dd", //nolint:gosec
		"if="+src,
		"of="+dst,
		"bs=1M",
		"conv=notrunc",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dd: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveFile implements QemuImgRunner.
func (ExecQemuImgRunner) RemoveFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Service is the disk lifecycle entry point used by the gRPC server
// and the auto-extend manager.
type Service struct {
	LVM     *lvm.Manager
	Libvirt libvirtx.Client
	Store   store.Store
	Locker  cluster.Locker
	QemuImg QemuImgRunner

	DefaultVG          string
	DefaultThinInitial uint64
	NodeID             string
	TempDir            string

	// Now returns the current time. Tests override it.
	Now func() time.Time

	mu sync.Mutex
}

// Options bundle the Service constructor inputs.
type Options struct {
	LVM     *lvm.Manager
	Libvirt libvirtx.Client
	Store   store.Store
	Locker  cluster.Locker
	QemuImg QemuImgRunner

	DefaultVG          string
	DefaultThinInitial uint64
	NodeID             string
	TempDir            string
}

// New constructs a Service. Required fields LVM, Libvirt, Store,
// Locker, NodeID must all be non-zero; QemuImg defaults to ExecQemuImgRunner.
func New(o Options) (*Service, error) {
	if o.LVM == nil || o.Libvirt == nil || o.Store == nil || o.Locker == nil {
		return nil, errors.New("disk.New: LVM, Libvirt, Store, and Locker are required")
	}
	if o.NodeID == "" {
		return nil, errors.New("disk.New: NodeID is required")
	}
	if o.QemuImg == nil {
		o.QemuImg = ExecQemuImgRunner{}
	}
	if o.TempDir == "" {
		o.TempDir = os.TempDir()
	}
	return &Service{
		LVM:                o.LVM,
		Libvirt:            o.Libvirt,
		Store:              o.Store,
		Locker:             o.Locker,
		QemuImg:            o.QemuImg,
		DefaultVG:          o.DefaultVG,
		DefaultThinInitial: o.DefaultThinInitial,
		NodeID:             o.NodeID,
		TempDir:            o.TempDir,
		Now:                func() time.Time { return time.Now().UTC() },
	}, nil
}

// CreateInput describes a CreateDisk call's effective parameters.
type CreateInput struct {
	Name             string
	VG               string
	Mode             store.ProvisionMode
	SizeBytes        uint64
	VirtualSizeBytes uint64
}

// validate normalises and checks the inputs against service defaults.
func (s *Service) normaliseCreate(in CreateInput) (CreateInput, error) {
	if in.Name == "" {
		return in, errors.New("disk: name is required")
	}
	if strings.ContainsAny(in.Name, " /\\\t\n") {
		return in, fmt.Errorf("disk: invalid name %q", in.Name)
	}
	if in.VG == "" {
		in.VG = s.DefaultVG
	}
	if in.VG == "" {
		return in, errors.New("disk: vg is required (and no default configured)")
	}
	switch in.Mode {
	case store.ProvisionUnspecified:
		in.Mode = store.ProvisionThick
	case store.ProvisionThick, store.ProvisionThin:
	default:
		return in, fmt.Errorf("disk: unsupported mode %v", in.Mode)
	}
	switch in.Mode {
	case store.ProvisionThick:
		if in.SizeBytes == 0 {
			return in, errors.New("disk: size_bytes is required for thick disks")
		}
		if in.VirtualSizeBytes == 0 {
			in.VirtualSizeBytes = in.SizeBytes
		}
	case store.ProvisionThin:
		if in.VirtualSizeBytes == 0 {
			return in, errors.New("disk: virtual_size_bytes is required for thin disks")
		}
		if in.SizeBytes == 0 {
			in.SizeBytes = s.DefaultThinInitial
		}
		if in.SizeBytes == 0 {
			return in, errors.New("disk: thin initial size_bytes is zero and no default configured")
		}
		if in.SizeBytes >= in.VirtualSizeBytes {
			return in, errors.New("disk: thin size_bytes must be < virtual_size_bytes")
		}
	}
	return in, nil
}

// CreateDisk provisions a new disk. For thick disks the LV is the only
// artifact. For thin disks a qcow2 header is written into the LV so
// QEMU sees a valid sparse image.
func (s *Service) CreateDisk(ctx context.Context, in CreateInput) (store.DiskRecord, error) {
	in, err := s.normaliseCreate(in)
	if err != nil {
		return store.DiskRecord{}, err
	}

	release, err := s.Locker.Acquire(ctx, in.VG)
	if err != nil {
		return store.DiskRecord{}, err
	}
	defer release()

	if _, err := s.Store.GetDisk(ctx, in.Name); err == nil {
		return store.DiskRecord{}, store.ErrAlreadyExists
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.DiskRecord{}, err
	}

	if err := s.LVM.CreateLV(ctx, in.VG, in.Name, in.SizeBytes); err != nil {
		return store.DiskRecord{}, fmt.Errorf("create lv: %w", err)
	}
	devicePath := lvm.DevicePath(in.VG, in.Name)

	if in.Mode == store.ProvisionThin {
		tmp, err := s.makeQcow2Header(ctx, in.Name, in.VirtualSizeBytes)
		if err != nil {
			_ = s.LVM.RemoveLV(ctx, in.VG, in.Name)
			return store.DiskRecord{}, err
		}
		defer func() { _ = s.QemuImg.RemoveFile(tmp) }()
		if err := s.QemuImg.DDOver(ctx, tmp, devicePath); err != nil {
			_ = s.LVM.RemoveLV(ctx, in.VG, in.Name)
			return store.DiskRecord{}, fmt.Errorf("dd into lv: %w", err)
		}
	}

	rec := store.DiskRecord{
		Name:             in.Name,
		VG:               in.VG,
		Mode:             in.Mode,
		SizeBytes:        in.SizeBytes,
		VirtualSizeBytes: in.VirtualSizeBytes,
		DevicePath:       devicePath,
		CreatedOnNode:    s.NodeID,
		CreatedAt:        s.Now(),
	}
	if err := s.Store.CreateDisk(ctx, rec); err != nil {
		_ = s.LVM.RemoveLV(ctx, in.VG, in.Name)
		return store.DiskRecord{}, fmt.Errorf("persist disk: %w", err)
	}
	return rec, nil
}

func (s *Service) makeQcow2Header(ctx context.Context, name string, virtualSize uint64) (string, error) {
	tmp := filepath.Join(s.TempDir, fmt.Sprintf("sltv-%s-%d.qcow2", name, time.Now().UnixNano()))
	if err := s.QemuImg.CreateQcow2(ctx, tmp, virtualSize); err != nil {
		return "", err
	}
	return tmp, nil
}

// DeleteDisk removes a disk. When force is true the disk is detached
// from any VMs first; otherwise the call fails if attachments exist.
func (s *Service) DeleteDisk(ctx context.Context, name string, force bool) error {
	rec, err := s.Store.GetDisk(ctx, name)
	if err != nil {
		return err
	}
	release, err := s.Locker.Acquire(ctx, rec.VG)
	if err != nil {
		return err
	}
	defer release()

	atts, err := s.Store.ListAttachments(ctx, name, "")
	if err != nil {
		return err
	}
	if len(atts) > 0 && !force {
		return fmt.Errorf("disk %q has %d attachments; use force to remove", name, len(atts))
	}
	for _, a := range atts {
		if err := s.detachLocked(ctx, rec, a); err != nil {
			return err
		}
	}
	if err := s.LVM.RemoveLV(ctx, rec.VG, rec.Name); err != nil {
		return fmt.Errorf("remove lv: %w", err)
	}
	if err := s.Store.DeleteDisk(ctx, name); err != nil {
		return err
	}
	return nil
}

// GetDisk returns a single record.
func (s *Service) GetDisk(ctx context.Context, name string) (store.DiskRecord, error) {
	return s.Store.GetDisk(ctx, name)
}

// ListDisks returns matching records.
func (s *Service) ListDisks(ctx context.Context, vg string) ([]store.DiskRecord, error) {
	return s.Store.ListDisks(ctx, vg)
}

// AttachInput describes an AttachDisk call.
type AttachInput struct {
	DiskName  string
	VMName    string
	TargetDev string
}

// AttachDisk hot-plugs the disk to vm. The target dev is auto-picked
// when empty (the first free /vd[a-z]/ slot is chosen by counting
// existing attachments for that VM in the catalog; the caller is
// responsible for ensuring the VM doesn't already have a disk on that
// slot outside SLTV's view, in which case libvirt will error).
func (s *Service) AttachDisk(ctx context.Context, in AttachInput) (store.AttachmentRecord, error) {
	if in.DiskName == "" || in.VMName == "" {
		return store.AttachmentRecord{}, errors.New("disk: disk_name and vm_name required")
	}
	rec, err := s.Store.GetDisk(ctx, in.DiskName)
	if err != nil {
		return store.AttachmentRecord{}, err
	}
	if exists, err := s.Libvirt.DomainExists(ctx, in.VMName); err != nil {
		return store.AttachmentRecord{}, err
	} else if !exists {
		return store.AttachmentRecord{}, fmt.Errorf("disk: libvirt domain %q not found", in.VMName)
	}
	if in.TargetDev == "" {
		atts, err := s.Store.ListAttachments(ctx, "", in.VMName)
		if err != nil {
			return store.AttachmentRecord{}, err
		}
		used := map[string]struct{}{"vda": {}}
		for _, a := range atts {
			used[a.TargetDev] = struct{}{}
		}
		in.TargetDev = pickTargetDev(used)
	}
	format := "raw"
	if rec.Mode == store.ProvisionThin {
		format = "qcow2"
	}
	if err := s.Libvirt.AttachDisk(ctx, in.VMName, libvirtx.DiskAttachSpec{
		SourceDevice: rec.DevicePath,
		TargetDev:    in.TargetDev,
		Format:       format,
		Persistent:   true,
		Live:         true,
	}); err != nil {
		return store.AttachmentRecord{}, fmt.Errorf("libvirt attach: %w", err)
	}
	att := store.AttachmentRecord{
		DiskName:   rec.Name,
		VMName:     in.VMName,
		Host:       s.NodeID,
		TargetDev:  in.TargetDev,
		AttachedAt: s.Now(),
	}
	if err := s.Store.CreateAttachment(ctx, att); err != nil {
		// Best-effort rollback so libvirt and store agree.
		_ = s.Libvirt.DetachDisk(ctx, in.VMName, in.TargetDev)
		return store.AttachmentRecord{}, err
	}
	return att, nil
}

// pickTargetDev returns the first /vd[a-z]/ name not in used.
func pickTargetDev(used map[string]struct{}) string {
	for c := byte('b'); c <= 'z'; c++ {
		name := "vd" + string([]byte{c})
		if _, ok := used[name]; !ok {
			return name
		}
	}
	// Out of letters; fall back to virtio-blk style indexed names.
	return "vdaa"
}

// DetachDisk removes a libvirt attachment and its store record.
func (s *Service) DetachDisk(ctx context.Context, disk, vm string) error {
	rec, err := s.Store.GetDisk(ctx, disk)
	if err != nil {
		return err
	}
	att, err := s.Store.GetAttachment(ctx, disk, vm)
	if err != nil {
		return err
	}
	return s.detachLocked(ctx, rec, att)
}

// detachLocked removes the disk from libvirt and the store. Caller
// must hold the VG lock when called from a delete loop; for direct
// detach calls we don't need a lock since libvirt/store mutations
// here are scoped to one VM.
func (s *Service) detachLocked(ctx context.Context, _ store.DiskRecord, att store.AttachmentRecord) error {
	if err := s.Libvirt.DetachDisk(ctx, att.VMName, att.TargetDev); err != nil {
		return fmt.Errorf("libvirt detach: %w", err)
	}
	if err := s.Store.DeleteAttachment(ctx, att.DiskName, att.VMName); err != nil {
		return err
	}
	return nil
}

// ListAttachments returns matching attachments.
func (s *Service) ListAttachments(ctx context.Context, disk, vm string) ([]store.AttachmentRecord, error) {
	return s.Store.ListAttachments(ctx, disk, vm)
}

// ExtendInput describes an ExtendDisk call.
type ExtendInput struct {
	Name       string
	SizeBytes  uint64
	DeltaBytes uint64
}

// ExtendDisk grows the disk's LV. SizeBytes and DeltaBytes are
// mutually exclusive; exactly one must be non-zero.
func (s *Service) ExtendDisk(ctx context.Context, in ExtendInput) (store.DiskRecord, error) {
	if (in.SizeBytes == 0) == (in.DeltaBytes == 0) {
		return store.DiskRecord{}, errors.New("disk: exactly one of size_bytes or delta_bytes must be set")
	}
	rec, err := s.Store.GetDisk(ctx, in.Name)
	if err != nil {
		return store.DiskRecord{}, err
	}
	release, err := s.Locker.Acquire(ctx, rec.VG)
	if err != nil {
		return store.DiskRecord{}, err
	}
	defer release()

	var newSize uint64
	if in.SizeBytes != 0 {
		newSize = in.SizeBytes
		if err := s.LVM.ExtendLVAbsolute(ctx, rec.VG, rec.Name, newSize); err != nil {
			return store.DiskRecord{}, err
		}
	} else {
		newSize = rec.SizeBytes + in.DeltaBytes
		if err := s.LVM.ExtendLVDelta(ctx, rec.VG, rec.Name, in.DeltaBytes); err != nil {
			return store.DiskRecord{}, err
		}
	}

	rec.SizeBytes = newSize
	rec.LastExtendedAt = s.Now()
	if err := s.Store.UpdateDisk(ctx, rec); err != nil {
		return store.DiskRecord{}, err
	}

	// Notify any libvirt domain that has this disk attached.
	atts, err := s.Store.ListAttachments(ctx, rec.Name, "")
	if err != nil {
		return rec, nil
	}
	for _, a := range atts {
		if err := s.Libvirt.BlockResize(ctx, a.VMName, a.TargetDev, newSize); err != nil {
			// Best-effort; the LV is already extended.
			_ = err
		}
	}
	return rec, nil
}

// ExtendByPercent grows the LV by the given percent of its current
// size. It is the entry point used by the auto-extend manager.
func (s *Service) ExtendByPercent(ctx context.Context, name string, percent int) (store.DiskRecord, error) {
	if percent <= 0 {
		return store.DiskRecord{}, errors.New("disk: percent must be > 0")
	}
	rec, err := s.Store.GetDisk(ctx, name)
	if err != nil {
		return store.DiskRecord{}, err
	}
	release, err := s.Locker.Acquire(ctx, rec.VG)
	if err != nil {
		return store.DiskRecord{}, err
	}
	defer release()

	if err := s.LVM.ExtendLVPercent(ctx, rec.VG, rec.Name, percent); err != nil {
		return store.DiskRecord{}, err
	}
	delta := rec.SizeBytes * uint64(percent) / 100
	rec.SizeBytes += delta
	rec.LastExtendedAt = s.Now()
	if err := s.Store.UpdateDisk(ctx, rec); err != nil {
		return store.DiskRecord{}, err
	}
	atts, _ := s.Store.ListAttachments(ctx, rec.Name, "")
	for _, a := range atts {
		_ = s.Libvirt.BlockResize(ctx, a.VMName, a.TargetDev, rec.SizeBytes)
	}
	return rec, nil
}
