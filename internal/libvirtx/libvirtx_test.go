package libvirtx

import (
	"context"
	"strings"
	"testing"
)

func TestBuildDiskXML(t *testing.T) {
	xml, err := buildDiskXML(DiskAttachSpec{
		SourceDevice: "/dev/vg-sltv/data1",
		TargetDev:    "vdb",
		Format:       "raw",
	})
	if err != nil {
		t.Fatalf("buildDiskXML: %v", err)
	}
	s := string(xml)
	for _, want := range []string{
		`<disk type="block" device="disk">`,
		`<driver name="qemu" type="raw" cache="none">`,
		`<source dev="/dev/vg-sltv/data1">`,
		`<target dev="vdb" bus="virtio">`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("xml missing %q\nfull: %s", want, s)
		}
	}
}

func TestBuildDiskXMLValidation(t *testing.T) {
	if _, err := buildDiskXML(DiskAttachSpec{TargetDev: "vda"}); err == nil {
		t.Error("expected error for missing source")
	}
	if _, err := buildDiskXML(DiskAttachSpec{SourceDevice: "/dev/vg/x"}); err == nil {
		t.Error("expected error for missing target dev")
	}
}

func TestParseDomstatsBlocks(t *testing.T) {
	out := []byte(`Domain: 'web01'
  block.count=2
  block.0.name=vda
  block.0.path=/dev/vg-sltv/web01-os
  block.0.allocation=1234
  block.0.capacity=10737418240
  block.0.physical=10737418240
  block.1.name=vdb
  block.1.path=/dev/vg-sltv/web01-data
  block.1.allocation=9223372036
  block.1.capacity=107374182400
  block.1.physical=10737418240
`)
	stats, err := parseDomstatsBlocks(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d stats, want 2", len(stats))
	}
	if stats[1].Name != "vdb" || stats[1].AllocationBytes != 9223372036 || stats[1].CapacityBytes != 107374182400 {
		t.Errorf("stats[1] = %+v", stats[1])
	}
	free := stats[1].FreePercent()
	// (10737418240 - 9223372036)/10737418240 * 100 ~= 14.1%
	if free < 13.5 || free > 14.5 {
		t.Errorf("free = %.2f, want ~14.1", free)
	}
}

func TestFakeClientAttachDetach(t *testing.T) {
	c := NewFakeClient("web01")
	ctx := context.Background()
	exists, err := c.DomainExists(ctx, "web01")
	if err != nil || !exists {
		t.Fatalf("expected web01 to exist, err=%v", err)
	}
	if err := c.AttachDisk(ctx, "web01", DiskAttachSpec{SourceDevice: "/dev/vg/x", TargetDev: "vdb", Format: "raw"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// Idempotent re-attach
	if err := c.AttachDisk(ctx, "web01", DiskAttachSpec{SourceDevice: "/dev/vg/x", TargetDev: "vdb", Format: "raw"}); err != nil {
		t.Fatalf("attach idempotent: %v", err)
	}
	// Conflict
	if err := c.AttachDisk(ctx, "web01", DiskAttachSpec{SourceDevice: "/dev/vg/y", TargetDev: "vdb", Format: "raw"}); err == nil {
		t.Errorf("expected conflict on different source")
	}
	if err := c.DetachDisk(ctx, "web01", "vdb"); err != nil {
		t.Fatalf("detach: %v", err)
	}
}

func TestFakeBlockResize(t *testing.T) {
	c := NewFakeClient("web01")
	c.Stats["web01"] = []BlockStats{{Index: 0, Name: "vdb", PhysicalBytes: 10}}
	if err := c.BlockResize(context.Background(), "web01", "vdb", 20); err != nil {
		t.Fatal(err)
	}
	if c.Stats["web01"][0].PhysicalBytes != 20 {
		t.Errorf("resize did not update stats: %+v", c.Stats["web01"])
	}
	if len(c.Resizes) != 1 {
		t.Errorf("resizes = %v", c.Resizes)
	}
}

func TestFreePercentZeroPhysical(t *testing.T) {
	if got := (BlockStats{}).FreePercent(); got != 100 {
		t.Errorf("FreePercent zero-physical = %v", got)
	}
}
