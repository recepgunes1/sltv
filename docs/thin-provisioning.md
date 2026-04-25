# Thin provisioning and auto-extend

Thin disks let a guest see a much larger volume than the host has
backed with physical storage. SLTV implements them by writing a
qcow2 image directly into a smaller LV: the guest sees the qcow2's
virtual size, but the LV (and thus the actual storage cost) only
grows on demand.

## Layout

```
+---------------------------+
| LV /dev/vg-sltv/db01-data |  <- physical, 10 GiB initially
|   ┌──────────────────┐    |
|   │ qcow2 header     │    |  <- written once at creation
|   │ allocated extents│    |  <- grow as the guest writes
|   └──────────────────┘    |
|     guest sees 100 GiB    |
+---------------------------+
```

When the guest writes new blocks, qcow2 allocates them at the head of
the image. As the qcow2's `physical` size approaches the LV's size
the guest is at risk of seeing I/O errors, so the daemon must extend
the LV before that happens.

## Creation flow

```bash
sctl create-disk db01-data --thin-prov --virtual-size 100G
```

1. Acquire the per-VG ETCD lock (no-op in standalone mode).
2. `lvcreate -L 10G -n db01-data vg-sltv` (initial size from
   `thin.default_initial_size`, override with `--size`).
3. `qemu-img create -f qcow2 /tmp/sltv-db01-data-...qcow2 100G`
4. `dd if=<tmp.qcow2> of=/dev/vg-sltv/db01-data bs=1M conv=notrunc`
5. Persist the disk record (`mode=THIN`, virtual=100 GiB, size=10 GiB).
6. Release the lock and clean up the temporary file.

## Auto-extend

The thin manager polls every `thin.poll_interval` seconds. For every
attachment whose host equals our `node_id`, it issues `virsh
domstats --block <vm>` (libvirt RPC) and reads three numbers per
disk:

- `physical` - bytes occupied on the host (effectively the LV size).
- `allocation` - bytes the guest has touched.
- `capacity` - the qcow2 virtual size.

The decision rule is:

```
free_pct = (physical - allocation) / physical * 100
if free_pct < free_threshold_percent:
    extend by extend_step_percent of the current LV size
    virDomainBlockResize so the guest sees the new size
```

In code, the trigger lives in
[`internal/thin/manager.go`](../internal/thin/manager.go) and the
extend itself in
[`internal/disk/disk.go::ExtendByPercent`](../internal/disk/disk.go).

### Tuning knobs

| Knob | Default | When to change |
| --- | --- | --- |
| `thin.free_threshold_percent` | 10 | Lower (e.g. 5) for steady-state workloads where you want to delay extends; raise (e.g. 20) for bursty workloads to keep more headroom. |
| `thin.extend_step_percent` | 10 | Increase to amortise the cost of extending under heavy I/O; decrease to keep allocation tighter. |
| `thin.poll_interval` | 15s | Lower for shorter reaction time at the cost of more `virsh domstats` calls; raise to reduce overhead. |
| `thin.default_initial_size` | 10 GiB | Bigger initial LV trades upfront capacity for fewer early extends. |

### Manual extend

If you need to grow a disk outside the auto-extend loop:

```bash
sctl extend-disk db01-data --delta +10G
sctl extend-disk db01-data --size 30G
```

Both forms acquire the VG lock, run `lvextend`, update the store, and
notify libvirt via `virDomainBlockResize`.

## Failure modes

- **`lvextend` fails (e.g. VG out of free extents).** The disk record
  is unchanged; the next tick retries. `sctl status` will surface the
  free space on the VG.
- **`virDomainBlockResize` fails.** The LV is already extended. The
  guest will not see the new capacity until libvirt accepts the
  resize, but the daemon log emits a warning and the next tick will
  re-issue the resize.
- **Polling falls behind under high I/O.** Reduce `poll_interval` and
  increase `extend_step_percent` so each extend buys more time.
