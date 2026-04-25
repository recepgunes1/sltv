#!/usr/bin/env bash
#
# Provision an ETCD node. Picks identity from $ETCD_NODE_INDEX (1..3).

set -euo pipefail

NODE_INDEX="${ETCD_NODE_INDEX:?ETCD_NODE_INDEX is required}"
NODE_NAME="sltv-etcd${NODE_INDEX}"
NODE_IP="192.168.56.$((20 + NODE_INDEX))"

CLUSTER="sltv-etcd1=http://192.168.56.21:2380,sltv-etcd2=http://192.168.56.22:2380,sltv-etcd3=http://192.168.56.23:2380"

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y etcd-server etcd-client

cat <<EOF >/etc/default/etcd
ETCD_NAME=${NODE_NAME}
ETCD_DATA_DIR=/var/lib/etcd
ETCD_LISTEN_PEER_URLS=http://${NODE_IP}:2380
ETCD_LISTEN_CLIENT_URLS=http://${NODE_IP}:2379,http://127.0.0.1:2379
ETCD_INITIAL_ADVERTISE_PEER_URLS=http://${NODE_IP}:2380
ETCD_ADVERTISE_CLIENT_URLS=http://${NODE_IP}:2379
ETCD_INITIAL_CLUSTER=${CLUSTER}
ETCD_INITIAL_CLUSTER_STATE=new
ETCD_INITIAL_CLUSTER_TOKEN=sltv-etcd-test
EOF

systemctl daemon-reload
systemctl enable --now etcd
