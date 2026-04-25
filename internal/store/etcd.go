package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// EtcdStore persists records in ETCD under a configurable prefix. It
// is the source of truth in cluster mode.
type EtcdStore struct {
	cli    *clientv3.Client
	prefix string
}

// NewEtcdStore wraps an existing etcd client. The given prefix is
// trimmed of trailing '/' and reused as the namespace; pass for
// example "/sltv/" to scope the catalog there.
func NewEtcdStore(cli *clientv3.Client, prefix string) *EtcdStore {
	if prefix == "" {
		prefix = "/sltv"
	}
	prefix = strings.TrimRight(prefix, "/")
	return &EtcdStore{cli: cli, prefix: prefix}
}

func (s *EtcdStore) diskKey(name string) string {
	return fmt.Sprintf("%s/disks/%s", s.prefix, name)
}

func (s *EtcdStore) disksPrefix() string {
	return fmt.Sprintf("%s/disks/", s.prefix)
}

func (s *EtcdStore) attachKey(disk, vm string) string {
	return fmt.Sprintf("%s/attachments/%s/%s", s.prefix, disk, vm)
}

func (s *EtcdStore) attachmentsPrefix() string {
	return fmt.Sprintf("%s/attachments/", s.prefix)
}

// CreateDisk implements Store using a Txn-with-If(NotExists) check.
func (s *EtcdStore) CreateDisk(ctx context.Context, r DiskRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	key := s.diskKey(r.Name)
	resp, err := s.cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, string(raw))).
		Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return ErrAlreadyExists
	}
	return nil
}

// UpdateDisk implements Store with an If(Exists) txn.
func (s *EtcdStore) UpdateDisk(ctx context.Context, r DiskRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	key := s.diskKey(r.Name)
	resp, err := s.cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), ">", 0)).
		Then(clientv3.OpPut(key, string(raw))).
		Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return ErrNotFound
	}
	return nil
}

// GetDisk implements Store.
func (s *EtcdStore) GetDisk(ctx context.Context, name string) (DiskRecord, error) {
	resp, err := s.cli.Get(ctx, s.diskKey(name))
	if err != nil {
		return DiskRecord{}, err
	}
	if len(resp.Kvs) == 0 {
		return DiskRecord{}, ErrNotFound
	}
	var r DiskRecord
	if err := json.Unmarshal(resp.Kvs[0].Value, &r); err != nil {
		return DiskRecord{}, err
	}
	return r, nil
}

// ListDisks implements Store.
func (s *EtcdStore) ListDisks(ctx context.Context, vg string) ([]DiskRecord, error) {
	resp, err := s.cli.Get(ctx, s.disksPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var out []DiskRecord
	for _, kv := range resp.Kvs {
		var r DiskRecord
		if err := json.Unmarshal(kv.Value, &r); err != nil {
			return nil, fmt.Errorf("decode %s: %w", string(kv.Key), err)
		}
		if vg != "" && r.VG != vg {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DeleteDisk implements Store.
func (s *EtcdStore) DeleteDisk(ctx context.Context, name string) error {
	_, err := s.cli.Delete(ctx, s.diskKey(name))
	return err
}

// CreateAttachment implements Store.
func (s *EtcdStore) CreateAttachment(ctx context.Context, a AttachmentRecord) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	key := s.attachKey(a.DiskName, a.VMName)
	resp, err := s.cli.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, string(raw))).
		Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return ErrAlreadyExists
	}
	return nil
}

// GetAttachment implements Store.
func (s *EtcdStore) GetAttachment(ctx context.Context, disk, vm string) (AttachmentRecord, error) {
	resp, err := s.cli.Get(ctx, s.attachKey(disk, vm))
	if err != nil {
		return AttachmentRecord{}, err
	}
	if len(resp.Kvs) == 0 {
		return AttachmentRecord{}, ErrNotFound
	}
	var a AttachmentRecord
	if err := json.Unmarshal(resp.Kvs[0].Value, &a); err != nil {
		return AttachmentRecord{}, err
	}
	return a, nil
}

// ListAttachments implements Store.
func (s *EtcdStore) ListAttachments(ctx context.Context, disk, vm string) ([]AttachmentRecord, error) {
	prefix := s.attachmentsPrefix()
	if disk != "" {
		prefix = prefix + disk + "/"
	}
	resp, err := s.cli.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var out []AttachmentRecord
	for _, kv := range resp.Kvs {
		var a AttachmentRecord
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return nil, err
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
func (s *EtcdStore) DeleteAttachment(ctx context.Context, disk, vm string) error {
	_, err := s.cli.Delete(ctx, s.attachKey(disk, vm))
	return err
}

// Close is a no-op for EtcdStore: the etcd client lifecycle is owned
// by the cluster package.
func (s *EtcdStore) Close() error { return nil }

// Compile-time interface satisfaction guard.
var _ Store = (*EtcdStore)(nil)

// staticErr forces the etcd client dependency to be referenced even
// when the etcd backend is unused at runtime, simplifying go.mod
// management.
var _ = errors.New
