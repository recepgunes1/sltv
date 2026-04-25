# SLTV - Shared Logical Thin Volume

SLTV is a storage controller for libvirt/QEMU-KVM hypervisors. It
manages LVM logical volumes and exposes them to virtual machines as
raw block disks, with support for both thick and thin provisioning.

The headline features are:

- Single host or multi-host operation. Multiple hypervisors can share
  one Volume Group on FC/iSCSI LUNs and coordinate metadata operations
  through an ETCD-backed distributed lock.
- Thin provisioning with automatic extend driven by QEMU block usage
  metrics. When a thin LV approaches its physical size the daemon
  grows it by a configurable percentage and notifies the guest via
  `virDomainBlockResize`, all without VM downtime.
- A clean separation between the daemon (`sltvd`) and the CLI (`sctl`)
  via a strongly-typed gRPC API.

## Components

| Binary  | Role                                                                       |
| ------- | -------------------------------------------------------------------------- |
| `sltvd` | The daemon. Runs as a systemd service on each hypervisor host.             |
| `sctl`  | The CLI. Talks to `sltvd` over gRPC (Unix socket by default).              |

## Build and install

Requirements: Go 1.25+, `lvm2`, `qemu-utils`, `libvirt-clients`,
`make` for the convenience targets.

```bash
make build
sudo install -m 0755 bin/sltvd /usr/local/sbin/sltvd
sudo install -m 0755 bin/sctl  /usr/local/bin/sctl
sudo install -D -m 0644 deploy/examples/sltvd.yaml /etc/sltv/sltvd.yaml
sudo install -D -m 0644 deploy/systemd/sltvd.service /etc/systemd/system/sltvd.service
sudo systemctl daemon-reload
sudo systemctl enable --now sltvd
sctl status
```

A multi-stage Docker image is also provided at
`deploy/docker/Dockerfile`; see [deploy/docker/README.md](deploy/docker/README.md).

## CLI cheat sheet

```bash
# Thick provisioned 50 GiB disk in the configured default VG
sctl create-disk web01-data --size 50G

# Thin provisioned, 100 GiB virtual, starts at 10 GiB on the LV
sctl create-disk db01-data --thin-prov --virtual-size 100G

# Attach / detach
sctl attach-disk web01-data web01            # picks vdb, vdc, ...
sctl attach-disk web01-data web01 --target vde
sctl detach-disk web01-data web01

# Manual extend
sctl extend-disk db01-data --delta +10G
sctl extend-disk db01-data --size 20G

# Inspect
sctl list-disks
sctl list-attachments
sctl status --cluster
```

`sctl` accepts `--addr unix:///run/sltv/sltvd.sock` (default) or a
TCP address with `--tls-cert/--tls-key/--tls-ca` for clustered TLS
deployments. All commands honour `--json`.

## Documentation

- [Architecture](docs/architecture.md)
- [Configuration reference](docs/configuration.md)
- [Clustering with ETCD](docs/clustering.md)
- [Thin provisioning and auto-extend](docs/thin-provisioning.md)
- [Testing guide](docs/testing.md)
- [API reference](docs/api.md) (the Swagger-equivalent for gRPC, generated from `api/proto/v1/sltv.proto`)

## Repository layout

```
api/proto/v1/        gRPC service definition
cmd/sltvd/           daemon entry point
cmd/sctl/            CLI entry point
internal/
  config/            YAML + env config loader
  cluster/           ETCD client, locks, membership
  disk/              disk lifecycle (thick + thin)
  libvirtx/          libvirt/virsh wrapper + fake
  lvm/               lvm2 wrapper + fake
  server/            gRPC service implementation
  store/             memory / bolt / etcd backends
  thin/              auto-extend control loop
  version/           build metadata
pkg/sltvclient/      reusable Go client
deploy/              systemd unit, sample config, Docker image
docs/                user and developer documentation
test/e2e/vagrant/    Vagrant + iSCSI + ETCD multi-host test bed
```

## Tests

```bash
go test ./...           # unit tests
make test-cover         # with coverage
SLTV_TEST_ETCD=http://127.0.0.1:2379 go test ./internal/cluster/...   # integration
```

## License

MIT - see [LICENSE](LICENSE).
