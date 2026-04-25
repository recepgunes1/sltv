# SLTV gRPC API Reference

This document is the human-readable reference for the SLTV gRPC API.
It is the Swagger-equivalent for a pure-gRPC service: the canonical
contract is the `.proto` file at
[api/proto/v1/sltv.proto](../api/proto/v1/sltv.proto). When you change
that file, regenerate the documentation either by hand-editing this
page or by running `make proto-docs` (which uses `protoc-gen-doc`).

- gRPC package: `sltv.v1`
- Go import path: `github.com/sltv/sltv/api/proto/v1`
- Default endpoints:
  - Unix socket: `unix:///run/sltv/sltvd.sock`
  - TCP (when enabled, mTLS required): `0.0.0.0:7443`

## Service: `Sltv`

| RPC | Request | Response | Description |
| --- | --- | --- | --- |
| `CreateDisk` | `CreateDiskRequest` | `Disk` | Provisions a new logical volume (and qcow2 image when thin). |
| `DeleteDisk` | `DeleteDiskRequest` | `google.protobuf.Empty` | Removes the LV after ensuring no attachments remain. |
| `GetDisk` | `GetDiskRequest` | `Disk` | Returns metadata about a single disk. |
| `ListDisks` | `ListDisksRequest` | `ListDisksResponse` | Returns all known disks, optionally filtered by VG. |
| `ExtendDisk` | `ExtendDiskRequest` | `Disk` | Manually grows the backing LV and notifies QEMU. |
| `AttachDisk` | `AttachDiskRequest` | `Attachment` | Hot-plugs the disk into a libvirt domain (PERSISTENT|LIVE). |
| `DetachDisk` | `DetachDiskRequest` | `google.protobuf.Empty` | Removes the disk from a libvirt domain. |
| `ListAttachments` | `ListAttachmentsRequest` | `ListAttachmentsResponse` | Returns matching attachments. |
| `GetNodeStatus` | `google.protobuf.Empty` | `NodeStatus` | Reports the local node's view. |
| `GetClusterStatus` | `google.protobuf.Empty` | `ClusterStatus` | Reports cluster-wide state when clustering is enabled. |

## Enums

### `ProvisionMode`

| Name | Value | Description |
| --- | --- | --- |
| `PROVISION_MODE_UNSPECIFIED` | 0 | Default placeholder; treated as THICK. |
| `PROVISION_MODE_THICK` | 1 | LV fully allocated up-front. |
| `PROVISION_MODE_THIN` | 2 | qcow2 image inside an undersized LV; auto-extended. |

## Messages

### `Disk`

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | Unique disk name (also the LV name). |
| `vg` | string | Volume group the LV lives in. |
| `mode` | ProvisionMode | Provisioning mode. |
| `size_bytes` | uint64 | Physical size of the backing LV in bytes. |
| `virtual_size_bytes` | uint64 | Guest-visible size in bytes. Equals `size_bytes` for THICK. |
| `device_path` | string | Path to the backing block device, e.g. `/dev/vg-sltv/web01-data`. |
| `created_on_node` | string | Node that originally created the disk (informational). |
| `created_at` | google.protobuf.Timestamp | Creation timestamp. |
| `last_extended_at` | google.protobuf.Timestamp | Last extend timestamp (THIN only). |

### `Attachment`

| Field | Type | Description |
| --- | --- | --- |
| `disk_name` | string | Name of the attached disk. |
| `vm_name` | string | libvirt domain name. |
| `host` | string | sltvd node where the domain runs. |
| `target_dev` | string | Target device inside the guest, e.g. `vdb`. |
| `attached_at` | google.protobuf.Timestamp | Attachment timestamp. |

### `NodeStatus`

| Field | Type | Description |
| --- | --- | --- |
| `node_id` | string | sltvd node identifier (defaults to hostname). |
| `version` | string | Build version string. |
| `cluster_enabled` | bool | Whether ETCD-based clustering is enabled. |
| `vgs` | repeated VolumeGroup | Volume groups visible on this node. |

### `VolumeGroup`

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | VG name. |
| `size_bytes` | uint64 | Total VG size. |
| `free_bytes` | uint64 | Free extents in bytes. |

### `ClusterStatus`

| Field | Type | Description |
| --- | --- | --- |
| `enabled` | bool | Whether clustering is enabled. |
| `leader` | string | Node id of the current leader (best-effort, advisory). |
| `nodes` | repeated NodeStatus | Known peers. |
| `disk_count` | uint32 | Total disks in the cluster catalog. |
| `attachment_count` | uint32 | Total attachments in the cluster catalog. |

### Request/response messages

#### `CreateDiskRequest`

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | Unique disk name. |
| `vg` | string | Optional VG; when empty the daemon's `default_vg` is used. |
| `mode` | ProvisionMode | Defaults to THICK if unspecified. |
| `size_bytes` | uint64 | THICK: total LV size. THIN: initial LV size (0 = configured default). |
| `virtual_size_bytes` | uint64 | THIN only: guest-visible disk size. Required for THIN. |

#### `DeleteDiskRequest`

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | Disk to remove. |
| `force` | bool | When true, detach from any attached VMs first. |

#### `GetDiskRequest`

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | Disk name. |

#### `ListDisksRequest`

| Field | Type | Description |
| --- | --- | --- |
| `vg` | string | Optional VG filter. |

#### `ListDisksResponse`

| Field | Type | Description |
| --- | --- | --- |
| `disks` | repeated Disk | Matching disks. |

#### `ExtendDiskRequest`

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | Disk name. |
| `size_bytes` | uint64 | Absolute new size; mutually exclusive with `delta_bytes`. |
| `delta_bytes` | uint64 | Relative growth; mutually exclusive with `size_bytes`. |

#### `AttachDiskRequest`

| Field | Type | Description |
| --- | --- | --- |
| `disk_name` | string | Disk to attach. |
| `vm_name` | string | Target libvirt domain name. |
| `target_dev` | string | Optional target device in the guest; auto-picked when empty. |

#### `DetachDiskRequest`

| Field | Type | Description |
| --- | --- | --- |
| `disk_name` | string | Disk to detach. |
| `vm_name` | string | Source libvirt domain name. |

#### `ListAttachmentsRequest`

| Field | Type | Description |
| --- | --- | --- |
| `disk_name` | string | Optional filter by disk. |
| `vm_name` | string | Optional filter by VM. |

#### `ListAttachmentsResponse`

| Field | Type | Description |
| --- | --- | --- |
| `attachments` | repeated Attachment | Matching attachments. |

## Error semantics

The service uses standard gRPC status codes:

| Code | When it appears |
| --- | --- |
| `OK` | Success. |
| `INVALID_ARGUMENT` | Required field missing, invalid size, both `size_bytes` and `delta_bytes` set, etc. |
| `ALREADY_EXISTS` | A disk or attachment with that key is already registered. |
| `NOT_FOUND` | Unknown disk, attachment, or VM. |
| `FAILED_PRECONDITION` | Cluster lock could not be acquired, or a precondition (e.g. disk attached) blocks the operation. |
| `RESOURCE_EXHAUSTED` | VG has insufficient free extents to satisfy the request. |
| `INTERNAL` | Unexpected lvm2/libvirt/qemu-img failure (the underlying error message is wrapped in the status detail). |
