#!/usr/bin/env bash
#
# Provision the iSCSI target VM. Exposes two LUNs (10G each) backed by
# loopback files; sltv hosts log in to /iqn.2026-04.local.sltv:test and
# share them as PVs in vg-shared.

set -euo pipefail

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y \
    targetcli-fb tgt python3-rtslib-fb open-iscsi

mkdir -p /var/lib/sltv-targets
truncate -s 10G /var/lib/sltv-targets/lun0.img
truncate -s 10G /var/lib/sltv-targets/lun1.img

cat <<'PY' >/tmp/setup-target.py
from rtslib_fb import (
    FabricModule, Target, TPG, NetworkPortal, FileIOStorageObject, LUN,
    NodeACL, RTSRoot,
)

iqn = "iqn.2026-04.local.sltv:test"
fm = FabricModule("iscsi")
fm.load()

# Idempotency: clear any existing target.
for tgt in list(fm.targets):
    if tgt.wwn == iqn:
        tgt.delete()

target = Target(fm, iqn)
tpg = TPG(target, 1)
tpg.enable = True
tpg.set_parameter("authentication", "0")
tpg.set_parameter("generate_node_acls", "1")
tpg.set_parameter("demo_mode_write_protect", "0")

NetworkPortal(tpg, "0.0.0.0", 3260)

for i, path in enumerate([
    "/var/lib/sltv-targets/lun0.img",
    "/var/lib/sltv-targets/lun1.img",
]):
    so = FileIOStorageObject(f"sltv-lun{i}", path, 10 * 1024 * 1024 * 1024)
    LUN(tpg, i, so)

RTSRoot().save_to_file()
print("targetcli configured")
PY

python3 /tmp/setup-target.py
systemctl enable --now rtslt-target.service 2>/dev/null || true
