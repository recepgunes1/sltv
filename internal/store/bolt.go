package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketDisks       = []byte("disks")
	bucketAttachments = []byte("attachments")
)

// BoltStore is a Store implementation backed by go.etcd.io/bbolt. It is
// the default for non-clustered sltvd nodes.
type BoltStore struct {
	db *bolt.DB
}

// OpenBoltStore opens or creates a BoltDB file at path. The parent
// directory is created with mode 0o755 when missing.
func OpenBoltStore(path string) (*BoltStore, error) {
	if path == "" {
		return nil, errors.New("store: bolt path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec
			return nil, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bolt %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketDisks, bucketAttachments} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &BoltStore{db: db}, nil
}

// Close implements Store.
func (s *BoltStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateDisk implements Store.
func (s *BoltStore) CreateDisk(_ context.Context, r DiskRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDisks)
		if b.Get([]byte(r.Name)) != nil {
			return ErrAlreadyExists
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return b.Put([]byte(r.Name), raw)
	})
}

// UpdateDisk implements Store.
func (s *BoltStore) UpdateDisk(_ context.Context, r DiskRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDisks)
		if b.Get([]byte(r.Name)) == nil {
			return ErrNotFound
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return b.Put([]byte(r.Name), raw)
	})
}

// GetDisk implements Store.
func (s *BoltStore) GetDisk(_ context.Context, name string) (DiskRecord, error) {
	var r DiskRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketDisks).Get([]byte(name))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, &r)
	})
	return r, err
}

// ListDisks implements Store.
func (s *BoltStore) ListDisks(_ context.Context, vg string) ([]DiskRecord, error) {
	var out []DiskRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDisks).ForEach(func(_, v []byte) error {
			var r DiskRecord
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if vg != "" && r.VG != vg {
				return nil
			}
			out = append(out, r)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DeleteDisk implements Store.
func (s *BoltStore) DeleteDisk(_ context.Context, name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDisks).Delete([]byte(name))
	})
}

// CreateAttachment implements Store.
func (s *BoltStore) CreateAttachment(_ context.Context, a AttachmentRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAttachments)
		key := []byte(AttachmentKey(a.DiskName, a.VMName))
		if b.Get(key) != nil {
			return ErrAlreadyExists
		}
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		return b.Put(key, raw)
	})
}

// GetAttachment implements Store.
func (s *BoltStore) GetAttachment(_ context.Context, disk, vm string) (AttachmentRecord, error) {
	var a AttachmentRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketAttachments).Get([]byte(AttachmentKey(disk, vm)))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, &a)
	})
	return a, err
}

// ListAttachments implements Store.
func (s *BoltStore) ListAttachments(_ context.Context, disk, vm string) ([]AttachmentRecord, error) {
	var out []AttachmentRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAttachments).ForEach(func(_, v []byte) error {
			var a AttachmentRecord
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			if disk != "" && a.DiskName != disk {
				return nil
			}
			if vm != "" && a.VMName != vm {
				return nil
			}
			out = append(out, a)
			return nil
		})
	})
	if err != nil {
		return nil, err
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
func (s *BoltStore) DeleteAttachment(_ context.Context, disk, vm string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAttachments).Delete([]byte(AttachmentKey(disk, vm)))
	})
}
