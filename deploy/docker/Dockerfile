# syntax=docker/dockerfile:1.6
#
# Multi-stage build for sltvd. The runtime image ships with lvm2,
# qemu-utils, and libvirt-clients so the daemon can shell out to
# lvcreate/lvextend/lvremove, qemu-img, dd, and virsh. To actually
# manipulate host LVs and libvirt domains, the container must be run
# privileged with the relevant host paths bind-mounted (see README).

ARG GO_VERSION=1.25
ARG DEBIAN_VERSION=bookworm-slim

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/sltv/sltv/internal/version.Version=${VERSION} -X github.com/sltv/sltv/internal/version.Commit=${COMMIT} -X github.com/sltv/sltv/internal/version.Date=${DATE}" \
    -o /out/sltvd ./cmd/sltvd
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/sltv/sltv/internal/version.Version=${VERSION}" \
    -o /out/sctl ./cmd/sctl

FROM debian:${DEBIAN_VERSION}
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        lvm2 \
        qemu-utils \
        libvirt-clients \
        coreutils \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/sltvd /usr/local/sbin/sltvd
COPY --from=build /out/sctl  /usr/local/bin/sctl
COPY deploy/examples/sltvd.yaml /etc/sltv/sltvd.yaml

VOLUME ["/var/lib/sltv", "/run/sltv"]

EXPOSE 7443

ENTRYPOINT ["/usr/local/sbin/sltvd"]
CMD ["-config", "/etc/sltv/sltvd.yaml"]
