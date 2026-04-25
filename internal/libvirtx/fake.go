package libvirtx

import (
	"context"
	"fmt"
	"sync"
)

// FakeClient is an in-memory Client implementation for unit tests.
// It records all calls and serves as a controllable double.
type FakeClient struct {
	mu sync.Mutex

	// Domains is the set of known VM names. DomainExists reports
	// membership.
	Domains map[string]struct{}

	// Stats maps vm -> block stats returned by DomainStats.
	Stats map[string][]BlockStats

	// Attached lists currently-attached disks per VM, keyed by target
	// dev. AttachDisk inserts; DetachDisk removes.
	Attached map[string]map[string]DiskAttachSpec

	// Resizes records BlockResize calls in order, per VM.
	Resizes []FakeResize

	// Err, when non-nil, is returned for the next call (and then cleared).
	NextErr error
}

// FakeResize records a BlockResize invocation.
type FakeResize struct {
	VM        string
	TargetDev string
	SizeBytes uint64
}

// NewFakeClient returns a FakeClient pre-populated with the given VMs.
func NewFakeClient(vms ...string) *FakeClient {
	f := &FakeClient{
		Domains:  map[string]struct{}{},
		Stats:    map[string][]BlockStats{},
		Attached: map[string]map[string]DiskAttachSpec{},
	}
	for _, vm := range vms {
		f.Domains[vm] = struct{}{}
		f.Attached[vm] = map[string]DiskAttachSpec{}
	}
	return f
}

func (f *FakeClient) takeErr() error {
	if f.NextErr != nil {
		err := f.NextErr
		f.NextErr = nil
		return err
	}
	return nil
}

// AttachDisk implements Client.
func (f *FakeClient) AttachDisk(_ context.Context, vm string, spec DiskAttachSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return err
	}
	if _, ok := f.Domains[vm]; !ok {
		return fmt.Errorf("fake: no domain %q", vm)
	}
	if _, ok := f.Attached[vm]; !ok {
		f.Attached[vm] = map[string]DiskAttachSpec{}
	}
	if existing, ok := f.Attached[vm][spec.TargetDev]; ok {
		// Idempotent: same source already attached -> no-op.
		if existing.SourceDevice == spec.SourceDevice {
			return nil
		}
		return fmt.Errorf("fake: %s already has %s attached", vm, spec.TargetDev)
	}
	f.Attached[vm][spec.TargetDev] = spec
	return nil
}

// DetachDisk implements Client.
func (f *FakeClient) DetachDisk(_ context.Context, vm, targetDev string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return err
	}
	if m, ok := f.Attached[vm]; ok {
		delete(m, targetDev)
	}
	return nil
}

// BlockResize implements Client.
func (f *FakeClient) BlockResize(_ context.Context, vm, targetDev string, sizeBytes uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return err
	}
	f.Resizes = append(f.Resizes, FakeResize{VM: vm, TargetDev: targetDev, SizeBytes: sizeBytes})
	if stats, ok := f.Stats[vm]; ok {
		for i := range stats {
			if stats[i].Name == targetDev {
				stats[i].PhysicalBytes = sizeBytes
				stats[i].CapacityBytes = sizeBytes
			}
		}
		f.Stats[vm] = stats
	}
	return nil
}

// DomainStats implements Client.
func (f *FakeClient) DomainStats(_ context.Context, vm string) ([]BlockStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return nil, err
	}
	return append([]BlockStats(nil), f.Stats[vm]...), nil
}

// DomainExists implements Client.
func (f *FakeClient) DomainExists(_ context.Context, vm string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return false, err
	}
	_, ok := f.Domains[vm]
	return ok, nil
}
