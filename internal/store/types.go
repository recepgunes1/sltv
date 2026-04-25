// Package store persists the SLTV disk catalog and attachment records.
//
// Two implementations are provided:
//
//   - MemoryStore: ephemeral, used in tests and as a building block.
//   - BoltStore:   on-disk via go.etcd.io/bbolt; default for standalone
//     sltvd nodes.
//   - EtcdStore:   ETCD-backed; the source of truth in cluster mode
//     (lives next to the cluster package and reuses the same client).
//
// The Store interface itself is intentionally narrow: the daemon does
// not need transactions across record types, so each method operates
// on a single key. This keeps the in-cluster implementation cheap
// (one ETCD round-trip per call) and makes it easy to swap backends.
package store

import (
	"errors"
	"time"
)

// ProvisionMode mirrors the proto enum but lives here so the store
// package has no proto dependency.
type ProvisionMode int

// Provisioning modes.
const (
	ProvisionUnspecified ProvisionMode = 0
	ProvisionThick       ProvisionMode = 1
	ProvisionThin        ProvisionMode = 2
)

// String returns a human-readable mode name.
func (p ProvisionMode) String() string {
	switch p {
	case ProvisionThick:
		return "thick"
	case ProvisionThin:
		return "thin"
	default:
		return "unspecified"
	}
}

// DiskRecord is the persisted form of a Disk.
type DiskRecord struct {
	Name             string        `json:"name"`
	VG               string        `json:"vg"`
	Mode             ProvisionMode `json:"mode"`
	SizeBytes        uint64        `json:"size_bytes"`
	VirtualSizeBytes uint64        `json:"virtual_size_bytes"`
	DevicePath       string        `json:"device_path"`
	CreatedOnNode    string        `json:"created_on_node"`
	CreatedAt        time.Time     `json:"created_at"`
	LastExtendedAt   time.Time     `json:"last_extended_at,omitempty"`
}

// AttachmentRecord is the persisted form of an Attachment.
type AttachmentRecord struct {
	DiskName   string    `json:"disk_name"`
	VMName     string    `json:"vm_name"`
	Host       string    `json:"host"`
	TargetDev  string    `json:"target_dev"`
	AttachedAt time.Time `json:"attached_at"`
}

// AttachmentKey is the stable composite key for attachments. Two
// attachments are equal iff (disk, vm) is equal: a disk can only be
// attached to a given VM once at a time.
func AttachmentKey(disk, vm string) string {
	return disk + "|" + vm
}

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("store: record not found")

// ErrAlreadyExists is returned by Create* methods when the key is taken.
var ErrAlreadyExists = errors.New("store: record already exists")
