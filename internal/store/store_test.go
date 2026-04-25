package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// suite asserts the Store contract for an arbitrary implementation.
func suite(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()

	r := DiskRecord{
		Name:          "data1",
		VG:            "vg-sltv",
		Mode:          ProvisionThick,
		SizeBytes:     10 * 1024 * 1024 * 1024,
		DevicePath:    "/dev/vg-sltv/data1",
		CreatedOnNode: "node-a",
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.CreateDisk(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreateDisk(ctx, r); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
	got, err := s.GetDisk(ctx, "data1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != r.Name || got.SizeBytes != r.SizeBytes {
		t.Errorf("got %+v want %+v", got, r)
	}
	r.SizeBytes = 20 * 1024 * 1024 * 1024
	if err := s.UpdateDisk(ctx, r); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = s.GetDisk(ctx, "data1")
	if got.SizeBytes != r.SizeBytes {
		t.Errorf("update did not apply: %+v", got)
	}
	r2 := r
	r2.Name = "data2"
	r2.VG = "vg-other"
	if err := s.CreateDisk(ctx, r2); err != nil {
		t.Fatalf("create r2: %v", err)
	}
	all, err := s.ListDisks(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %v %v", all, err)
	}
	one, err := s.ListDisks(ctx, "vg-sltv")
	if err != nil || len(one) != 1 || one[0].Name != "data1" {
		t.Fatalf("list filtered: %v %v", one, err)
	}
	if err := s.DeleteDisk(ctx, "data1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetDisk(ctx, "data1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if err := s.DeleteDisk(ctx, "no-such"); err != nil {
		t.Errorf("delete missing should be no-op, got %v", err)
	}

	a := AttachmentRecord{
		DiskName:   "data2",
		VMName:     "web01",
		Host:       "node-a",
		TargetDev:  "vdb",
		AttachedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.CreateAttachment(ctx, a); err != nil {
		t.Fatalf("create attach: %v", err)
	}
	if err := s.CreateAttachment(ctx, a); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
	a2 := a
	a2.VMName = "web02"
	if err := s.CreateAttachment(ctx, a2); err != nil {
		t.Fatalf("create attach 2: %v", err)
	}
	got2, err := s.GetAttachment(ctx, "data2", "web01")
	if err != nil || got2.VMName != "web01" {
		t.Errorf("get attachment: %+v %v", got2, err)
	}
	allA, err := s.ListAttachments(ctx, "data2", "")
	if err != nil || len(allA) != 2 {
		t.Fatalf("list attach: %v %v", allA, err)
	}
	one2, err := s.ListAttachments(ctx, "", "web02")
	if err != nil || len(one2) != 1 {
		t.Fatalf("list attach filtered: %v %v", one2, err)
	}
	if err := s.DeleteAttachment(ctx, "data2", "web01"); err != nil {
		t.Fatalf("delete attach: %v", err)
	}
	if _, err := s.GetAttachment(ctx, "data2", "web01"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()
	suite(t, s)
}

func TestBoltStore(t *testing.T) {
	s, err := OpenBoltStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	suite(t, s)
}

func TestBoltStorePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	s, err := OpenBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDisk(context.Background(), DiskRecord{Name: "x", VG: "v", SizeBytes: 1, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.GetDisk(context.Background(), "x")
	if err != nil || got.Name != "x" {
		t.Fatalf("expected disk x to persist; got %+v err=%v", got, err)
	}
}
