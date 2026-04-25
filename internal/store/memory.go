package store

import (
	"context"
	"sort"
	"sync"
)

// MemoryStore is an in-memory Store implementation. It is goroutine-safe.
type MemoryStore struct {
	mu          sync.RWMutex
	disks       map[string]DiskRecord
	attachments map[string]AttachmentRecord
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		disks:       map[string]DiskRecord{},
		attachments: map[string]AttachmentRecord{},
	}
}

// CreateDisk implements Store.
func (s *MemoryStore) CreateDisk(_ context.Context, r DiskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.disks[r.Name]; ok {
		return ErrAlreadyExists
	}
	s.disks[r.Name] = r
	return nil
}

// UpdateDisk implements Store.
func (s *MemoryStore) UpdateDisk(_ context.Context, r DiskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.disks[r.Name]; !ok {
		return ErrNotFound
	}
	s.disks[r.Name] = r
	return nil
}

// GetDisk implements Store.
func (s *MemoryStore) GetDisk(_ context.Context, name string) (DiskRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.disks[name]
	if !ok {
		return DiskRecord{}, ErrNotFound
	}
	return r, nil
}

// ListDisks implements Store.
func (s *MemoryStore) ListDisks(_ context.Context, vg string) ([]DiskRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DiskRecord, 0, len(s.disks))
	for _, d := range s.disks {
		if vg != "" && d.VG != vg {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DeleteDisk implements Store.
func (s *MemoryStore) DeleteDisk(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.disks, name)
	return nil
}

// CreateAttachment implements Store.
func (s *MemoryStore) CreateAttachment(_ context.Context, a AttachmentRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := AttachmentKey(a.DiskName, a.VMName)
	if _, ok := s.attachments[key]; ok {
		return ErrAlreadyExists
	}
	s.attachments[key] = a
	return nil
}

// GetAttachment implements Store.
func (s *MemoryStore) GetAttachment(_ context.Context, disk, vm string) (AttachmentRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.attachments[AttachmentKey(disk, vm)]
	if !ok {
		return AttachmentRecord{}, ErrNotFound
	}
	return r, nil
}

// ListAttachments implements Store.
func (s *MemoryStore) ListAttachments(_ context.Context, disk, vm string) ([]AttachmentRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AttachmentRecord, 0, len(s.attachments))
	for _, a := range s.attachments {
		if disk != "" && a.DiskName != disk {
			continue
		}
		if vm != "" && a.VMName != vm {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DiskName != out[j].DiskName {
			return out[i].DiskName < out[j].DiskName
		}
		return out[i].VMName < out[j].VMName
	})
	return out, nil
}

// DeleteAttachment implements Store.
func (s *MemoryStore) DeleteAttachment(_ context.Context, disk, vm string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attachments, AttachmentKey(disk, vm))
	return nil
}

// Close implements Store. It is a no-op for MemoryStore.
func (s *MemoryStore) Close() error { return nil }
