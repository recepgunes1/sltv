# sltvd Docker image

The image at `deploy/docker/Dockerfile` packages `sltvd` and `sctl`
together with `lvm2`, `qemu-utils`, and `libvirt-clients`. Building:

```bash
docker build -f deploy/docker/Dockerfile -t sltv/sltvd:dev .
```

To run the daemon as a storage controller on a host, the container
needs visibility of the host's block devices, libvirt socket, and
configuration directory:

```bash
docker run --rm -d \
  --name sltvd \
  --privileged \
  --network host \
  -v /dev:/dev \
  -v /run/libvirt:/run/libvirt \
  -v /etc/lvm:/etc/lvm \
  -v /var/lib/sltv:/var/lib/sltv \
  -v /run/sltv:/run/sltv \
  -v /etc/sltv:/etc/sltv:ro \
  sltv/sltvd:dev
```

Then talk to it from `sctl` (either via the host install or
`docker exec sltvd sctl ...`):

```bash
sctl --addr unix:///run/sltv/sltvd.sock list-disks
```

The image also runs in cluster mode when the mounted `/etc/sltv/sltvd.yaml`
points at an ETCD cluster.
