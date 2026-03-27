# Tests Directory Guide

This directory contains test manifests and operation guides.

## kind Setup and Validation

### Prerequisites

- `kind`, `kubectl`, and `docker` are installed
- Run commands from repository root

### 1) Create cluster

```bash
kind create cluster --name fuse-dev --config .devcontainer/kind-config.yaml
```

Expected:

- cluster `fuse-dev` is created

### 2) Build images

```bash
docker build -t fuse-csi-driver:latest ./fuse-csi-driver/
docker build -t sshfs-sidecar:latest ./sshfs-sidecar/
docker build -t s3fs-sidecar:latest ./s3fs-sidecar/
```

Expected:

- all 3 images build successfully

### 3) Load images into kind

```bash
kind load docker-image fuse-csi-driver:latest --name fuse-dev
kind load docker-image sshfs-sidecar:latest --name fuse-dev
kind load docker-image s3fs-sidecar:latest --name fuse-dev
```

Expected:

- images are available on kind nodes

### 4) Deploy CSI driver

```bash
kubectl apply -f csi/fuse-csi-driver.yaml
kubectl apply -f csi/fuse-csi-driver-daemonset.yaml
kubectl -n fuse-csi-system rollout status ds/fuse-csi-driver --timeout=180s
```

Expected:

- `fuse-csi-driver` DaemonSet becomes ready

### 5) Run automated kind test

```bash
.devcontainer/test.sh
```

Expected:

- script completes and verifies mount/write flow

### 6) Manual quick check

```bash
kubectl get pods -A
kubectl -n fuse-csi-system get pods -l app=fuse-csi-driver -o wide
```

Expected:

- CSI pods are running

### 7) Cleanup (optional)

```bash
kind delete cluster --name fuse-dev
```

## Other Files

- `test-ssh-server.yaml`: SSH backend for sshfs test
- `test-minio.yaml`: MinIO backend for s3fs test
- `manual-check-rke2.txt`: manual validation steps for RKE2
- `install-uninstall-rke2.txt`: quick install/uninstall checklist
- `test-report-rke2.txt`: recorded test output summary
