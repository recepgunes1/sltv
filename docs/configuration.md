# Configuration reference

`sltvd` is configured via a single YAML file at
`/etc/sltv/sltvd.yaml` (override with `-config <path>`). All keys
shown below are optional; missing keys fall back to the documented
defaults. Environment variables prefixed `SLTV_` override individual
fields and are useful in containers where you do not want to ship a
file.

A complete annotated example lives at
[`deploy/examples/sltvd.yaml`](../deploy/examples/sltvd.yaml).

## Schema

```yaml
listen:
  unix: /run/sltv/sltvd.sock     # UNIX socket; empty disables it
  tcp: ""                         # e.g. "0.0.0.0:7443"; requires TLS

tls:
  cert: ""                        # PEM cert for TCP listener
  key:  ""                        # PEM private key
  ca:   ""                        # optional client CA bundle for mTLS

storage:
  state_dir: /var/lib/sltv        # holds bolt state.db, tmp files
  default_vg: vg-sltv             # used when CreateDisk omits a VG

libvirt:
  uri: qemu:///system             # libvirt connection URI

thin:
  default_initial_size: 10GiB     # initial LV size for thin disks
  free_threshold_percent: 10      # auto-extend trigger
  extend_step_percent: 10         # each extend grows by this much
  poll_interval: 15s              # cadence of the auto-extend loop

cluster:
  enabled: false                  # turn on ETCD-backed clustering
  node_id: ""                     # defaults to hostname
  etcd:
    endpoints: []                 # required when enabled
    lock_ttl: 10s
    tls:
      cert: ""
      key:  ""
      ca:   ""

log:
  level: info                     # debug | info | warn | error
  format: json                    # json | text
```

## Field reference

### `listen`

| Field | Default | Description |
| --- | --- | --- |
| `unix` | `/run/sltv/sltvd.sock` | UNIX socket path. The directory is created with mode `0o755` and the socket has mode `0o660`. |
| `tcp`  | `""`                   | Optional TCP listener. Requires `tls.cert` and `tls.key`. |

At least one of `unix` and `tcp` must be set.

### `tls`

Used only by the TCP listener and by ETCD when `cluster.etcd.tls` is empty.

| Field | Description |
| --- | --- |
| `cert` | Server certificate in PEM. |
| `key`  | Server private key in PEM. |
| `ca`   | Optional CA bundle. When set, mTLS is required (`RequireAndVerifyClientCert`). |

### `storage`

| Field | Default | Description |
| --- | --- | --- |
| `state_dir`  | `/var/lib/sltv` | Holds the BoltDB store and temporary qcow2 headers. |
| `default_vg` | `vg-sltv`       | VG used when `CreateDiskRequest.vg` is empty. |

### `libvirt`

| Field | Default | Description |
| --- | --- | --- |
| `uri` | `qemu:///system` | Passed to `virsh -c <uri>`. |

### `thin`

Controls auto-extend behaviour. Sizes accept human-readable strings
(`10GiB`, `512M`, `1024`).

| Field | Default | Description |
| --- | --- | --- |
| `default_initial_size`     | `10GiB` | LV size used when `CreateDisk` for a thin disk omits `size_bytes`. |
| `free_threshold_percent`   | `10`    | When the LV's free space drops below this, the daemon extends. Must be in `(0, 100)`. |
| `extend_step_percent`      | `10`    | Each extend grows the LV by this percentage of the *current* size. |
| `poll_interval`            | `15s`   | How often `domstats` is consulted. |

### `cluster`

| Field | Default | Description |
| --- | --- | --- |
| `enabled`              | `false` | Turn on ETCD-backed clustering. |
| `node_id`              | hostname | Stable identifier for this `sltvd`. |
| `etcd.endpoints`       | `[]`    | List of ETCD endpoints. Required when `enabled`. |
| `etcd.lock_ttl`        | `10s`   | TTL for the ETCD session backing per-VG mutexes. |
| `etcd.tls.{cert,key,ca}` | `""`  | Optional mTLS to ETCD. |

### `log`

| Field | Default | Description |
| --- | --- | --- |
| `level`  | `info`  | `debug`, `info`, `warn`, `error`. |
| `format` | `json`  | `json` or `text`. |

## Environment overrides

The following overrides are applied after the YAML file is parsed.

| Variable | Field |
| --- | --- |
| `SLTV_LISTEN_UNIX`     | `listen.unix` |
| `SLTV_LISTEN_TCP`      | `listen.tcp` |
| `SLTV_STATE_DIR`       | `storage.state_dir` |
| `SLTV_DEFAULT_VG`      | `storage.default_vg` |
| `SLTV_LIBVIRT_URI`     | `libvirt.uri` |
| `SLTV_NODE_ID`         | `cluster.node_id` |
| `SLTV_CLUSTER_ENABLED` | `cluster.enabled` (truthy strings) |
| `SLTV_ETCD_ENDPOINTS`  | `cluster.etcd.endpoints` (comma-separated) |
| `SLTV_LOG_LEVEL`       | `log.level` |
| `SLTV_LOG_FORMAT`      | `log.format` |

## Validation

`sltvd` rejects start-up if any of the following are violated:

- At least one of `listen.unix` and `listen.tcp` must be set.
- A non-empty `listen.tcp` requires `tls.cert` and `tls.key`.
- `storage.state_dir` must not be empty.
- `thin.free_threshold_percent` must be in `(0, 100)`.
- `thin.extend_step_percent` must be in `(0, 1000]`.
- `thin.poll_interval` must be at least `1s`.
- `cluster.enabled=true` requires non-empty `cluster.etcd.endpoints`
  and `cluster.etcd.lock_ttl >= 1s`.
