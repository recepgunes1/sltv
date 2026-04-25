#!/usr/bin/env bash
#
# End-to-end scenarios run against the Vagrant cluster from the host.
# Requires: vagrant up to have completed for sltv-host{1..3}.

set -euo pipefail

H1="vagrant ssh sltv-host1 -c"
H2="vagrant ssh sltv-host2 -c"
H3="vagrant ssh sltv-host3 -c"

echo "=== Scenario 1: parallel create-disk ==="
$H1 'sudo sctl --addr unix:///run/sltv/sltvd.sock create-disk d-thick-1 --size 1G' &
$H2 'sudo sctl --addr unix:///run/sltv/sltvd.sock create-disk d-thick-2 --size 1G' &
$H3 'sudo sctl --addr unix:///run/sltv/sltvd.sock create-disk d-thick-3 --size 1G' &
wait

echo "=== Scenario 2: list-disks from any host should see all three ==="
$H1 'sudo sctl --addr unix:///run/sltv/sltvd.sock list-disks'
$H2 'sudo sctl --addr unix:///run/sltv/sltvd.sock list-disks'

echo "=== Scenario 3: thin provision + write to trigger auto-extend ==="
$H1 'sudo sctl --addr unix:///run/sltv/sltvd.sock create-disk d-thin-1 --thin-prov --virtual-size 4G'
$H1 'sudo virsh list --all'
# Attach to a nested VM, write 800 MiB, watch sltvd auto-extend.

echo "=== Scenario 4: host failure ==="
echo "Run 'vagrant halt sltv-host3' and verify the rest of the cluster"
echo "still serves CreateDisk after the lock TTL expires."

echo "=== Scenario 5: detach + delete cleanup ==="
$H1 'sudo sctl --addr unix:///run/sltv/sltvd.sock delete-disk d-thick-1 --force'
$H2 'sudo sctl --addr unix:///run/sltv/sltvd.sock delete-disk d-thick-2 --force'
$H3 'sudo sctl --addr unix:///run/sltv/sltvd.sock delete-disk d-thick-3 --force'
echo "All scenarios completed."
