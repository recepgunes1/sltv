#!/usr/bin/env bash
#
# Provision an sltv hypervisor host:
#   - install libvirtd, lvm2, qemu-utils, open-iscsi
#   - log in to the shared iSCSI target
#   - on host #1, run pvcreate/vgcreate exactly once
#   - build sltvd from the rsynced source tree at /vagrant
#   - install systemd unit and start sltvd in cluster mode

set -euo pipefail

NODE_INDEX="${SLTV_NODE_INDEX:?SLTV_NODE_INDEX is required}"
NODE_NAME="sltv-host${NODE_INDEX}"

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y \
    qemu-kvm libvirt-daemon-system libvirt-clients qemu-utils \
    lvm2 open-iscsi golang-go ca-certificates

systemctl enable --now libvirtd

# iSCSI login
sed -i 's/^node.startup =.*/node.startup = automatic/' /etc/iscsi/iscsid.conf
systemctl enable --now iscsid open-iscsi
iscsiadm -m discovery -t st -p 192.168.56.10 || true
iscsiadm -m node -p 192.168.56.10 -l || true
sleep 3

# Wait for /dev/sd* to appear
for _ in $(seq 30); do
    DEV1=$(lsblk -ndo NAME,TRAN | awk '$2=="iscsi"{print "/dev/"$1; exit}' || true)
    [ -n "${DEV1:-}" ] && break
    sleep 1
done
DEV1=$(lsblk -ndo NAME,TRAN | awk '$2=="iscsi"{print "/dev/"$1}' | head -n1)

if [ -n "${DEV1:-}" ] && [ "$NODE_INDEX" = "1" ]; then
    if ! vgs vg-shared >/dev/null 2>&1; then
        pvcreate -ff -y "$DEV1"
        vgcreate vg-shared "$DEV1"
    fi
fi
# Other hosts simply need the VG to be visible after pvscan/vgscan.
vgscan --cache || true

# Build sltvd/sctl from source.
mkdir -p /opt/sltv
cd /vagrant
go build -o /usr/local/sbin/sltvd ./cmd/sltvd
go build -o /usr/local/bin/sctl ./cmd/sctl
install -D -m 0644 deploy/systemd/sltvd.service /etc/systemd/system/sltvd.service

# Install configuration with cluster mode enabled.
mkdir -p /etc/sltv
cat <<EOF >/etc/sltv/sltvd.yaml
listen:
  unix: /run/sltv/sltvd.sock
storage:
  state_dir: /var/lib/sltv
  default_vg: vg-shared
libvirt:
  uri: qemu:///system
thin:
  default_initial_size: 1GiB
  free_threshold_percent: 20
  extend_step_percent: 10
  poll_interval: 5s
cluster:
  enabled: true
  node_id: ${NODE_NAME}
  etcd:
    endpoints:
      - http://192.168.56.21:2379
      - http://192.168.56.22:2379
      - http://192.168.56.23:2379
    lock_ttl: 10s
log:
  level: info
  format: text
EOF

mkdir -p /run/sltv /var/lib/sltv
systemctl daemon-reload
systemctl enable --now sltvd
sleep 2
sctl status --addr unix:///run/sltv/sltvd.sock || true
