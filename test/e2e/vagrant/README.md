# SLTV e2e test bed

This directory contains a Vagrantfile and provisioning scripts that
spin up a 3-host SLTV cluster sharing one iSCSI target with one
3-node ETCD cluster, mirroring a small production deployment.

For full instructions and scenarios, see [docs/testing.md](../../../docs/testing.md).

Quick start:

```bash
cd test/e2e/vagrant
vagrant up sltv-target sltv-etcd1 sltv-etcd2 sltv-etcd3 sltv-host1 sltv-host2 sltv-host3
./run-scenarios.sh
```
