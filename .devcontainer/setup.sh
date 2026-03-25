#!/bin/bash
set -euo pipefail

# kind インストール
if ! command -v kind &>/dev/null; then
  echo "=== kind インストール中 ==="
  KIND_VERSION=$(curl -s https://api.github.com/repos/kubernetes-sigs/kind/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
  curl -Lo /usr/local/bin/kind "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64"
  chmod +x /usr/local/bin/kind
  echo "kind ${KIND_VERSION} インストール完了"
fi

# Go モジュールキャッシュの事前取得
echo "=== Go 依存関係を取得中 ==="
(cd /workspaces/fuse_k8s/fuse-csi-driver && go mod download)
(cd /workspaces/fuse_k8s/sshfs-sidecar && go mod download)
(cd /workspaces/fuse_k8s/s3fs-sidecar && go mod download)

echo "=== セットアップ完了 ==="
