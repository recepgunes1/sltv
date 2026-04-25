package store

import "context"

// Store is the persistent catalog used by sltvd.
//
// All methods take a context.Context to allow callers to bound them
// when running against a remote backend (ETCD).
type Store interface {
	// CreateDisk inserts r and returns ErrAlreadyExists if the name
	// is already taken.
	CreateDisk(ctx context.Context, r DiskRecord) error
	// UpdateDisk replaces an existing record by name; ErrNotFound when
	// it does not exist.
	UpdateDisk(ctx context.Context, r DiskRecord) error
	// GetDisk fetches a single record.
	GetDisk(ctx context.Context, name string) (DiskRecord, error)
	// ListDisks returns all disks. When vg!="" only matching VGs are
	// returned.
	ListDisks(ctx context.Context, vg string) ([]DiskRecord, error)
	// DeleteDisk removes a disk record. Removing a non-existent record
	// is a no-op (return nil).
	DeleteDisk(ctx context.Context, name string) error

	// CreateAttachment inserts a, returning ErrAlreadyExists for an
	// existing (disk, vm) pair.
	CreateAttachment(ctx context.Context, a AttachmentRecord) error
	// GetAttachment fetches one attachment by (disk, vm).
	GetAttachment(ctx context.Context, disk, vm string) (AttachmentRecord, error)
	// ListAttachments returns matching attachments. Either or both of
	// disk/vm may be empty to widen the filter.
	ListAttachments(ctx context.Context, disk, vm string) ([]AttachmentRecord, error)
	// DeleteAttachment removes an attachment; non-existent is a no-op.
	DeleteAttachment(ctx context.Context, disk, vm string) error

	// Close releases resources. Idempotent.
	Close() error
}
