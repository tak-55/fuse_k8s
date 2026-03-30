# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

This repository implements FUSE filesystem mounts (sshfs, s3fs) inside Kubernetes Pods while maintaining **PSS (Pod Security Standards) restricted** compliance. It contains two architectures:

| | Old (sidecar, `old/`) | New (fuse-csi-driver, current) |
|--|--|--|
| PSS restricted | ❌ requires privileged | ✅ fully compliant |
| User Pod sidecar | privileged: true required | Non-privileged initContainer |
| Mount mechanism | sidecar runs sshfs/s3fs | CSI DaemonSet mounts, passes fd to sidecar |
| Target | Development | Multi-tenant production (Capsule + Kyverno) |

## Build Commands

```bash
# Build CSI driver (new architecture)
cd fuse-csi-driver && docker build -t fuse-csi-driver:latest .

# Build sidecars
docker build -t sshfs-sidecar:latest ./sshfs-sidecar/
docker build -t s3fs-sidecar:latest ./s3fs-sidecar/

# Load into kind cluster
kind load docker-image fuse-csi-driver:latest --name fuse-dev
kind load docker-image sshfs-sidecar:latest --name fuse-dev
kind load docker-image s3fs-sidecar:latest --name fuse-dev
```

## Deploy Commands

```bash
# Create kind cluster (uses kind-config.yaml which exposes /dev/fuse to nodes)
kind create cluster --name fuse-dev --config .devcontainer/kind-config.yaml

# Deploy CSI driver (one-time setup)
kubectl apply -f csi/fuse-csi-driver.yaml          # Namespace + CSIDriver resource
kubectl apply -f csi/fuse-csi-driver-daemonset.yaml # kind variant
# For kubeadm production: fuse-csi-driver-daemonset-prod.yaml
# For RKE2:   fuse-csi-driver-daemonset-rke2.yaml  (/var/lib/rancher/rke2/agent/kubelet)
# For k3s:    fuse-csi-driver-daemonset-k3s.yaml   (/var/lib/rancher/k3s/agent/kubelet)

kubectl get pods -n fuse-csi-system

# Deploy user Pods
kubectl apply -f sshfs/deploy-kind.yaml
kubectl apply -f s3fs/deploy-kind.yaml

# Debug
kubectl logs <pod> -c sshfs-sidecar
kubectl exec <pod> -c app -- mount | grep fuse
```

## Architecture: fd-passing (new)

The key innovation: a privileged CSI DaemonSet opens `/dev/fuse` and mounts the filesystem, then passes the FUSE file descriptor to a non-privileged sidecar via Unix Domain Socket (UDS) + SCM_RIGHTS. User Pods need no privileges.

```
[User Pod — PSS restricted, hostUsers: false]
  initContainer: create-fuse-device → touch /dev/fuse (makes libfuse fall back to fusermount3)
  initContainer: sshfs-sidecar (restartPolicy: Always, UID 1000)
    receiver (Go) ──UDS── receives fd from CSI DaemonSet
    fusermount3-stub → intercepts libfuse mount calls, returns pre-opened fd
    sshfs/s3fs daemon
  container: app → accesses /data (FUSE mount propagated via kubelet bind-mount)

[CSI DaemonSet — fuse-csi-system, privileged: true]
  NodePublishVolume:
    1. open("/dev/fuse") → FUSE fd
    2. mount(2) at targetPath
    3. Listen on UDS: /fuse-fd/csi.sock
    4. Send fd + params (JSON) via SCM_RIGHTS to sidecar
    Manages up to 50 concurrent mounts
```

**Why `touch /dev/fuse`**: Without a real character device, libfuse cannot open `/dev/fuse` directly and falls back to calling `fusermount3`. The stub replaces the real `fusermount3` binary and injects the pre-opened fd via `FUSE_PREOPEN_FD` env var.

## Key Component Relationships

- **`fuse-csi-driver/pkg/driver/node.go`** — `NodePublishVolume` / `NodeUnpublishVolume` implementation; dispatches to sshfs or s3fs based on `volumeContext["type"]`
- **`fuse-csi-driver/pkg/driver/fdpassing.go`** — UDS server that sends the FUSE fd to sidecars
- **`sshfs-sidecar/receiver/main.go`** — UDS client that receives the fd, sets `FUSE_PREOPEN_FD`, and execs sshfs
- **`sshfs-sidecar/fusermount3-stub/main.go`** — Replaces `/usr/bin/fusermount3`; reads `FUSE_PREOPEN_FD` and returns that fd to libfuse instead of mounting

## DaemonSet Variants

Choose the correct DaemonSet manifest based on the kubelet socket path:

| File | Kubelet path | Use for |
|--|--|--|
| `fuse-csi-driver-daemonset.yaml` | `/var/lib/kubelet` | kind / devcontainer |
| `fuse-csi-driver-daemonset-prod.yaml` | `/var/lib/kubelet` | kubeadm |
| `fuse-csi-driver-daemonset-rke2.yaml` | `/var/lib/rancher/rke2/agent/kubelet` | RKE2 |
| `fuse-csi-driver-daemonset-k3s.yaml` | `/var/lib/rancher/k3s/agent/kubelet` | k3s |

## Secret Management (new architecture)

Secrets are referenced via `volumeAttributes` in the CSI ephemeral volume. The CSI driver writes them to temp files at mount time.

```bash
# sshfs — SSH private key（必ず --from-file を使うこと）
# --from-literal はシェル展開で末尾改行を除去するため "error in libcrypto" が発生する
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/id_ed25519 \
  -n <tenant-namespace>

# s3fs — S3 credentials
kubectl create secret generic s3-credentials \
  --from-literal=access_key=KEY --from-literal=secret_key=SECRET \
  -n <tenant-namespace>
```

## Security Policies (multi-tenant)

- **`policy/capsule-tenant-example.yaml`** — Capsule Tenant with PSA `enforce: restricted`
- **`policy/kyverno-force-securecontext.yaml`** — Mutates Pods to inject `runAsNonRoot: true`, `runAsUser: 1000`, `seccompProfile: RuntimeDefault`, `capabilities.drop: [ALL]`

## CI/CD

GitHub Actions (`.github/workflows/docker-image.yml`) builds multi-platform images (linux/amd64, linux/arm64) on push to main and publishes to `ghcr.io/tak-55/fuse_k8s-*:latest` with date-sha tags.

## Adding a New FUSE Filesystem

Use `sshfs-sidecar/` as a template:
1. New sidecar directory with `Dockerfile` (same multi-stage pattern: Go builder + ubuntu:22.04 with FUSE tool)
2. `receiver/main.go` — receives fd, sets env, execs the FUSE daemon
3. `fusermount3-stub/main.go` — copy from sshfs-sidecar (identical logic)
4. CSI driver: add a new `case` in `node.go` for `volumeContext["type"]`
5. Add to the GitHub Actions matrix in `docker-image.yml`
