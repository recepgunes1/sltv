# Integration tests

Integration tests live next to the packages they exercise but are
gated on environment variables so they only run on Linux CI runners
that have the required tooling.

| Test | Trigger | Requirements |
| --- | --- | --- |
| `internal/cluster.TestManagerEndToEnd` | `SLTV_TEST_ETCD=http://127.0.0.1:2379` | a running etcd v3 node |
| Loopback LVM tests (planned) | `SLTV_TEST_LOOP=1` | root, lvm2, losetup |

The unit-test suite (`go test ./...`) does not require any of these.
