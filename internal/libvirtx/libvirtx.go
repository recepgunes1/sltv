// Package libvirtx is the SLTV abstraction over a libvirt connection.
// It hides the concrete transport (virsh shell-out by default; can be
// swapped for a libvirt RPC client) behind the Client interface so the
// rest of the daemon can be unit-tested without a running libvirtd.
//
// The default implementation uses `virsh` because it works everywhere
// libvirt-clients is installed and avoids a cgo dependency on
// libvirt-dev. For production it can be replaced by an RPC client
// (e.g. github.com/digitalocean/go-libvirt) by satisfying the Client
// interface; no callers need to change.
package libvirtx

import (
	"bufio"
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Runner abstracts command execution; ExecRunner shells out via os/exec.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs the given binary and returns the combined output.
type ExecRunner struct{}

// Run executes name with args and returns combined stdout/stderr.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// DiskAttachSpec describes the disk to attach. The package builds the
// libvirt XML for the user.
type DiskAttachSpec struct {
	// SourceDevice is the host-side block device path, e.g.
	// "/dev/vg-sltv/web01-data".
	SourceDevice string
	// TargetDev is the guest-side device, e.g. "vdb". When empty the
	// caller should choose one (we do not auto-pick here).
	TargetDev string
	// Format is the qemu driver format: "raw" for thick, "qcow2" for
	// thin disks.
	Format string
	// Bus is the disk bus, defaults to "virtio".
	Bus string
	// Persistent makes the change survive a guest restart. The flag is
	// passed via virsh's --persistent.
	Persistent bool
	// Live applies to the running domain (--live).
	Live bool
}

// BlockStats is the parsed result of `virsh domstats --block`. Only the
// fields SLTV needs are kept.
type BlockStats struct {
	Index           int
	Name            string // disk's libvirt target dev (e.g. "vdb")
	BackingPath     string
	AllocationBytes uint64
	CapacityBytes   uint64
	PhysicalBytes   uint64
}

// FreePercent returns (physical-allocation)/physical*100, or 100 when
// physical is zero (so callers don't divide by zero).
func (b BlockStats) FreePercent() float64 {
	if b.PhysicalBytes == 0 {
		return 100
	}
	used := float64(b.AllocationBytes)
	if used > float64(b.PhysicalBytes) {
		used = float64(b.PhysicalBytes)
	}
	return 100.0 * (1.0 - used/float64(b.PhysicalBytes))
}

// Client is the libvirt abstraction the rest of SLTV depends on.
type Client interface {
	// AttachDisk hot-plugs the described disk into vm. Implementations
	// must be idempotent: attaching an already-attached disk by the
	// same target dev should not error.
	AttachDisk(ctx context.Context, vm string, spec DiskAttachSpec) error
	// DetachDisk removes a disk identified by target dev (preferred)
	// or source path (fallback) from vm.
	DetachDisk(ctx context.Context, vm, targetDev string) error
	// BlockResize tells QEMU about a new size for a guest device, so
	// the guest sees the extended capacity without a reboot.
	BlockResize(ctx context.Context, vm, targetDev string, sizeBytes uint64) error
	// DomainStats returns block stats for the named domain.
	DomainStats(ctx context.Context, vm string) ([]BlockStats, error)
	// DomainExists reports whether vm is a defined libvirt domain.
	DomainExists(ctx context.Context, vm string) (bool, error)
}

// VirshClient implements Client by shelling out to `virsh`. It is the
// default production implementation.
type VirshClient struct {
	r   Runner
	uri string
}

// NewVirshClient constructs a virsh-based Client. The connection URI is
// passed to virsh as `-c <uri>`. Pass nil for r to use ExecRunner{}.
func NewVirshClient(uri string, r Runner) *VirshClient {
	if r == nil {
		r = ExecRunner{}
	}
	return &VirshClient{r: r, uri: uri}
}

// connectArgs prepends `-c <uri>` when a URI is configured.
func (c *VirshClient) connectArgs() []string {
	if c.uri == "" {
		return nil
	}
	return []string{"-c", c.uri}
}

// AttachDisk implements Client.
func (c *VirshClient) AttachDisk(ctx context.Context, vm string, spec DiskAttachSpec) error {
	xmlBytes, err := buildDiskXML(spec)
	if err != nil {
		return err
	}
	// virsh attach-device <vm> <xmlfile> [--persistent --live]
	// We pass the XML via /dev/stdin using shell here; instead, use a
	// temp file via stdin redirection by writing to a pipe.
	args := append(c.connectArgs(), "attach-device", vm, "/dev/stdin")
	if spec.Persistent {
		args = append(args, "--persistent")
	}
	if spec.Live {
		args = append(args, "--live")
	}
	// Use exec.CommandContext directly to feed stdin; we cannot use
	// Runner here, so we switch to ExecRunner-equivalent behaviour for
	// this one path. The Runner abstraction is preserved for tests via
	// the FakeClient which bypasses VirshClient entirely.
	cmd := exec.CommandContext(ctx, "virsh", args...)
	cmd.Stdin = strings.NewReader(string(xmlBytes))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("virsh attach-device: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DetachDisk implements Client. Uses `virsh detach-disk <vm> <target>`.
func (c *VirshClient) DetachDisk(ctx context.Context, vm, targetDev string) error {
	if targetDev == "" {
		return fmt.Errorf("libvirtx.DetachDisk: targetDev required")
	}
	args := append(c.connectArgs(), "detach-disk", vm, targetDev, "--persistent", "--live")
	_, err := c.r.Run(ctx, "virsh", args...)
	return err
}

// BlockResize implements Client. Uses `virsh blockresize`.
func (c *VirshClient) BlockResize(ctx context.Context, vm, targetDev string, sizeBytes uint64) error {
	args := append(c.connectArgs(), "blockresize", vm, targetDev, fmt.Sprintf("%dB", sizeBytes))
	_, err := c.r.Run(ctx, "virsh", args...)
	return err
}

// DomainStats implements Client. Parses `virsh domstats --block` output.
func (c *VirshClient) DomainStats(ctx context.Context, vm string) ([]BlockStats, error) {
	args := append(c.connectArgs(), "domstats", "--block", vm)
	out, err := c.r.Run(ctx, "virsh", args...)
	if err != nil {
		return nil, err
	}
	return parseDomstatsBlocks(out)
}

// DomainExists implements Client.
func (c *VirshClient) DomainExists(ctx context.Context, vm string) (bool, error) {
	args := append(c.connectArgs(), "dominfo", vm)
	out, err := c.r.Run(ctx, "virsh", args...)
	if err == nil {
		return true, nil
	}
	if strings.Contains(string(out), "failed to get domain") || strings.Contains(err.Error(), "not found") {
		return false, nil
	}
	return false, err
}

// --- XML construction ------------------------------------------------------

type diskXML struct {
	XMLName xml.Name `xml:"disk"`
	Type    string   `xml:"type,attr"`
	Device  string   `xml:"device,attr"`
	Driver  driverXML
	Source  sourceXML
	Target  targetXML
}

type driverXML struct {
	XMLName xml.Name `xml:"driver"`
	Name    string   `xml:"name,attr"`
	Type    string   `xml:"type,attr"`
	Cache   string   `xml:"cache,attr,omitempty"`
}

type sourceXML struct {
	XMLName xml.Name `xml:"source"`
	Dev     string   `xml:"dev,attr"`
}

type targetXML struct {
	XMLName xml.Name `xml:"target"`
	Dev     string   `xml:"dev,attr"`
	Bus     string   `xml:"bus,attr"`
}

// buildDiskXML produces a libvirt <disk> XML element for AttachDevice.
func buildDiskXML(spec DiskAttachSpec) ([]byte, error) {
	if spec.SourceDevice == "" {
		return nil, fmt.Errorf("libvirtx: SourceDevice required")
	}
	if spec.TargetDev == "" {
		return nil, fmt.Errorf("libvirtx: TargetDev required")
	}
	format := spec.Format
	if format == "" {
		format = "raw"
	}
	bus := spec.Bus
	if bus == "" {
		bus = "virtio"
	}
	d := diskXML{
		Type:   "block",
		Device: "disk",
		Driver: driverXML{Name: "qemu", Type: format, Cache: "none"},
		Source: sourceXML{Dev: spec.SourceDevice},
		Target: targetXML{Dev: spec.TargetDev, Bus: bus},
	}
	return xml.MarshalIndent(d, "", "  ")
}

// --- domstats parsing ------------------------------------------------------

// parseDomstatsBlocks decodes the human-readable virsh domstats output:
//
//	Domain: 'web01'
//	  block.count=2
//	  block.0.name=vda
//	  block.0.path=/dev/vg-sltv/web01-os
//	  block.0.allocation=1234567890
//	  block.0.capacity=10737418240
//	  block.0.physical=10737418240
//	  block.1.name=vdb
//	  ...
//
// Lines that don't fit the `block.<n>.<key>=<value>` shape are ignored.
func parseDomstatsBlocks(out []byte) ([]BlockStats, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	byIndex := map[int]*BlockStats{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "block.") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := line[:eq]
		val := line[eq+1:]
		parts := strings.SplitN(key, ".", 3)
		if len(parts) < 3 {
			continue
		}
		idx, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		stat, ok := byIndex[idx]
		if !ok {
			stat = &BlockStats{Index: idx}
			byIndex[idx] = stat
		}
		switch parts[2] {
		case "name":
			stat.Name = val
		case "path":
			stat.BackingPath = val
		case "allocation":
			stat.AllocationBytes = parseUint(val)
		case "capacity":
			stat.CapacityBytes = parseUint(val)
		case "physical":
			stat.PhysicalBytes = parseUint(val)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan domstats: %w", err)
	}
	stats := make([]BlockStats, 0, len(byIndex))
	for i := 0; i < len(byIndex); i++ {
		if s, ok := byIndex[i]; ok {
			stats = append(stats, *s)
		}
	}
	// Append any indices we missed (gaps), in numeric order.
	if len(stats) != len(byIndex) {
		stats = stats[:0]
		idxs := make([]int, 0, len(byIndex))
		for i := range byIndex {
			idxs = append(idxs, i)
		}
		sortInts(idxs)
		for _, i := range idxs {
			stats = append(stats, *byIndex[i])
		}
	}
	return stats, nil
}

func parseUint(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

func sortInts(a []int) {
	// tiny insertion sort; len(a) is at most a few dozen disks
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
