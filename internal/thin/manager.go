// Package thin runs the auto-extend control loop for thin-provisioned
// SLTV disks. It periodically queries libvirt for block stats and
// calls disk.Service.ExtendByPercent when a disk's LV is filling up.
//
// The decision rule (mirrored from the project brief):
//
//	free_pct = (physical - allocation) / physical * 100
//	if free_pct < free_threshold_percent:
//	    extend by extend_step_percent of the current LV size
//
// Only attachments hosted on this node are inspected (the
// `Attachment.Host == NodeID` filter), because libvirt is local. In
// cluster mode each sltvd instance manages the VMs that run on its
// own host; the LV mutation itself is serialised by the cluster lock
// inside disk.Service.
package thin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sltv/sltv/internal/disk"
	"github.com/sltv/sltv/internal/libvirtx"
	"github.com/sltv/sltv/internal/store"
)

// Manager owns the polling goroutine. Use New to create one and call
// Run to start it; Stop terminates the loop.
type Manager struct {
	disks   *disk.Service
	libvirt libvirtx.Client
	st      store.Store
	logger  *slog.Logger

	pollInterval         time.Duration
	freeThresholdPercent int
	extendStepPercent    int
	nodeID               string

	stopOnce sync.Once
	cancel   context.CancelFunc
	doneCh   chan struct{}
}

// Options configure a Manager.
type Options struct {
	Disks                *disk.Service
	Libvirt              libvirtx.Client
	Store                store.Store
	Logger               *slog.Logger
	PollInterval         time.Duration
	FreeThresholdPercent int
	ExtendStepPercent    int
	NodeID               string
}

// New constructs a Manager.
func New(o Options) (*Manager, error) {
	if o.Disks == nil || o.Libvirt == nil || o.Store == nil {
		return nil, errors.New("thin: Disks, Libvirt, and Store are required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 15 * time.Second
	}
	if o.FreeThresholdPercent <= 0 || o.FreeThresholdPercent >= 100 {
		return nil, fmt.Errorf("thin: free_threshold_percent must be in (0, 100), got %d", o.FreeThresholdPercent)
	}
	if o.ExtendStepPercent <= 0 {
		return nil, errors.New("thin: extend_step_percent must be > 0")
	}
	if o.NodeID == "" {
		return nil, errors.New("thin: NodeID is required")
	}
	return &Manager{
		disks:                o.Disks,
		libvirt:              o.Libvirt,
		st:                   o.Store,
		logger:               o.Logger,
		pollInterval:         o.PollInterval,
		freeThresholdPercent: o.FreeThresholdPercent,
		extendStepPercent:    o.ExtendStepPercent,
		nodeID:               o.NodeID,
	}, nil
}

// Run starts the polling loop. It blocks until ctx is cancelled or
// Stop is called. The Manager is single-shot: once Stop has been
// invoked, callers must construct a new Manager.
func (m *Manager) Run(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx) //nolint:gosec
	m.cancel = cancel
	m.doneCh = make(chan struct{})
	defer close(m.doneCh)

	t := time.NewTicker(m.pollInterval)
	defer t.Stop()

	// Run one immediate sweep so freshly-extended disks are accurate.
	m.tick(runCtx)
	for {
		select {
		case <-runCtx.Done():
			return
		case <-t.C:
			m.tick(runCtx)
		}
	}
}

// Stop signals the loop to terminate and waits for it.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		if m.doneCh != nil {
			<-m.doneCh
		}
	})
}

// Tick performs a single inspection pass. Exposed for tests so we can
// drive the loop deterministically.
func (m *Manager) Tick(ctx context.Context) {
	m.tick(ctx)
}

func (m *Manager) tick(ctx context.Context) {
	atts, err := m.st.ListAttachments(ctx, "", "")
	if err != nil {
		m.logger.Warn("thin: list attachments", "err", err)
		return
	}
	// Group attachments by VM so we make one DomainStats call each.
	byVM := map[string][]store.AttachmentRecord{}
	for _, a := range atts {
		if a.Host != "" && a.Host != m.nodeID {
			continue
		}
		byVM[a.VMName] = append(byVM[a.VMName], a)
	}
	for vm, vmAtts := range byVM {
		stats, err := m.libvirt.DomainStats(ctx, vm)
		if err != nil {
			m.logger.Warn("thin: domstats failed", "vm", vm, "err", err)
			continue
		}
		statsByDev := map[string]libvirtx.BlockStats{}
		for _, s := range stats {
			statsByDev[s.Name] = s
		}
		for _, a := range vmAtts {
			rec, err := m.st.GetDisk(ctx, a.DiskName)
			if err != nil {
				continue
			}
			if rec.Mode != store.ProvisionThin {
				continue
			}
			s, ok := statsByDev[a.TargetDev]
			if !ok {
				continue
			}
			free := s.FreePercent()
			m.logger.Debug("thin: inspect", "vm", vm, "disk", rec.Name, "target", a.TargetDev,
				"free_pct", free, "physical", s.PhysicalBytes, "allocation", s.AllocationBytes)
			if free >= float64(m.freeThresholdPercent) {
				continue
			}
			before := rec.SizeBytes
			updated, err := m.disks.ExtendByPercent(ctx, rec.Name, m.extendStepPercent)
			if err != nil {
				m.logger.Warn("thin: extend failed", "disk", rec.Name, "err", err)
				continue
			}
			m.logger.Info("thin: extended",
				"disk", rec.Name, "vm", vm, "target", a.TargetDev,
				"old_size", before, "new_size", updated.SizeBytes, "free_pct", free)
		}
	}
}
