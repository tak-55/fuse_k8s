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
```

## Deploy Commands

```bash
# Deploy CSI driver (one-time setup)
kubectl apply -f csi/fuse-csi-driver.yaml               # Namespace + CSIDriver resource
kubectl apply -f csi/fuse-csi-driver-daemonset-prod.yaml # kubeadm / RKE2 / k3s

kubectl get pods -n fuse-csi-system

# Deploy user Pods (examples/ はローカル変更が git pull で上書きされないサンプル置き場)
kubectl apply -f examples/sshfs/deploy.yaml -n <tenant-namespace>
kubectl apply -f examples/s3fs/deploy.yaml  -n <tenant-namespace>

# Or reapply the local-only overlays after git pull
kubectl apply -k overlays/local/prod

# Debug
kubectl logs <pod> -c sshfs-sidecar
kubectl exec <pod> -c app -- mount | grep fuse
```

## Architecture: fd-passing (proxy/pull モデル)

The key innovation: a privileged CSI DaemonSet opens `/dev/fuse` and mounts the filesystem, then passes the FUSE file descriptor to a non-privileged sidecar via Unix Domain Socket (UDS) + SCM_RIGHTS. User Pods need no privileges.

```
[User Pod — PSS restricted, hostUsers: false]
  initContainer: create-fuse-device → touch /dev/fuse (makes libfuse fall back to fusermount3)
  initContainer: sshfs-sidecar (restartPolicy: Always, UID 1000)
    receiver (Go) → creds.json 読み込み → FUSERMOUNT3PROXY_FDPASSING_SOCKPATH セット → sshfs exec
    fusermount3-proxy → libfuse から呼ばれ CSI UDS に接続 → SCM_RIGHTS で fd 受信 → libfuse に返す
    sshfs/s3fs daemon
  container: app → accesses /data (FUSE mount propagated via kubelet bind-mount)

[CSI DaemonSet — fuse-csi-system, privileged: true]
  NodePublishVolume:
    1. open("/dev/fuse") → FUSE fd
    2. mount(2) at targetPath
    3. params.json + creds.json を emptyDir に書き出し（creds.json: 0600, chown 1000:1000）
    4. Listen on UDS: /fuse-fd/csi.sock（fd のみ SCM_RIGHTS で送信）
    Manages up to 50 concurrent mounts
```

**Why `touch /dev/fuse`**: Without a real character device, libfuse cannot open `/dev/fuse` directly and falls back to calling `fusermount3`. The proxy replaces the real `fusermount3` binary and pulls the fd from the CSI UDS socket via `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH`.

## Key Component Relationships

- **`fuse-csi-driver/pkg/driver/node.go`** — `NodePublishVolume` / `NodeUnpublishVolume` implementation; dispatches to sshfs or s3fs based on `volumeContext["type"]`
- **`fuse-csi-driver/pkg/driver/fdpassing.go`** — writes creds.json to emptyDir; UDS server that sends the FUSE fd (only) to fusermount3-proxy
- **`sshfs-sidecar/receiver/main.go`** — reads creds.json from emptyDir, sets `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH`, and execs sshfs
- **`sshfs-sidecar/fusermount3-proxy/main.go`** — Replaces `/usr/bin/fusermount3`; connects to CSI UDS, receives fd via SCM_RIGHTS, and returns it to libfuse via `_FUSE_COMMFD`

## DaemonSet Variants

| File | Kubelet path | Use for |
|--|--|--|
| `fuse-csi-driver-daemonset-prod.yaml` | `/var/lib/kubelet` | kubeadm / RKE2 / k3s |

## Secret Management (new architecture)

Secrets are referenced via `volumeAttributes` in the CSI ephemeral volume. The CSI driver writes them to `creds.json` in the pod's emptyDir volume (mode 0600, chown 1000:1000).

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

GitHub Actions (`.github/workflows/docker-image.yml`) builds multi-platform images (linux/amd64, linux/arm64) on push to main and publishes to `ghcr.io/tak-55/{fuse-csi-driver,sshfs-sidecar,s3fs-sidecar}:latest` with date-sha tags.

## Adding a New FUSE Filesystem

Use `sshfs-sidecar/` as a template:
1. New sidecar directory with `Dockerfile` (same multi-stage pattern: Go builder + ubuntu:22.04 with FUSE tool)
2. `receiver/main.go` — reads creds.json, sets `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH`, execs the FUSE daemon
3. `fusermount3-proxy/main.go` — copy from sshfs-sidecar (identical logic)
4. CSI driver: add a new `case` in `node.go` for `volumeContext["type"]`
5. Add to the GitHub Actions matrix in `docker-image.yml`
6. Add sample manifest to `examples/<name>/deploy.yaml` with `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` env set
