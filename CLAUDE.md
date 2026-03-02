# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 概要

meta-fuse-csi-plugin を使って Kubernetes Pod 内で FUSE ファイルシステム（sshfs・s3fs）を安全にマウントする実装。アプリコンテナに `CAP_SYS_ADMIN` を付与せず、DaemonSet に集約することでセキュリティを確保している。

## よく使うコマンド

### ローカルビルド（kind 開発環境）

```bash
# イメージビルド
docker build -t sshfs-proxy:latest ./sshfs/
docker build -t s3fs-proxy:latest ./s3fs/

# kind クラスターへのロード
kind load docker-image sshfs-proxy:latest --name fuse-dev
kind load docker-image s3fs-proxy:latest --name fuse-dev
```

### CSI ドライバーのデプロイ（初回のみ）

```bash
kubectl apply -f csi/csi-driver.yaml
kubectl apply -f csi/csi-driver-daemonset.yaml
kubectl get pods -n mfcp-system
```

### Pod のデプロイと確認

```bash
# kind 環境
kubectl apply -f sshfs/deploy-kind.yaml
kubectl apply -f s3fs/deploy-kind.yaml

# レジストリ使用時
kubectl apply -f sshfs/deploy-registry.yaml
kubectl apply -f s3fs/deploy-registry.yaml

# 動作確認
kubectl logs <pod-name> -c sshfs-proxy   # サイドカーログ
kubectl exec <pod-name> -c app -- mount | grep fuse
kubectl logs -n mfcp-system -l app=meta-fuse-csi-plugin
```

### kind クラスター管理

```bash
kind create cluster --name fuse-dev
kind delete cluster --name fuse-dev
```

## アーキテクチャ

### マウントの仕組み

```
[User Pod]
  initContainers (sidecar, privileged: true)
    entrypoint.sh
      touch /dev/fuse          ← libfuse を fusermount3 経由にフォールバックさせる重要な処理
      sshfs/s3fs -f            ← フォアグラウンド起動
      fusermount3-proxy ──UDS──→ [CSI DaemonSet (CAP_SYS_ADMIN)]
                                    open("/dev/fuse") + mount()
  containers (app)
    /data ← HostToContainer mountPropagation で伝播
```

**`touch /dev/fuse` が重要**: これにより libfuse がキャラクターデバイスとして open できず、fusermount3 経由（= fusermount3-proxy）にフォールバックする。これがプロキシ通信の起点。

### Dockerfile の構造（sshfs・s3fs 共通パターン）

マルチステージビルド:
1. `golang:1.20.7` で `fusermount3-proxy` をビルド（pfnet-research/meta-fuse-csi-plugin から）
2. `ubuntu:22.04` に FUSE ツール（sshfs または s3fs）をインストールし、`fusermount3-proxy` を `/bin/fusermount3` として配置

### deploy-kind.yaml vs deploy-registry.yaml の違い

| | deploy-kind.yaml | deploy-registry.yaml |
|--|--|--|
| image | `sshfs-proxy:latest`（ローカル） | `ghcr.io/tak-lab55/fuse_k8s-sshfs:latest` |
| imagePullPolicy | `Never` | `Always` |

### Secret の管理パターン

**sshfs**:
```bash
kubectl create secret generic ssh-key --from-file=private_key=${HOME}/.ssh/sshfs_key
```
→ entrypoint.sh が `/root/.ssh/private_key` (600) として書き出す

**s3fs**:
```bash
kubectl create secret generic s3-credentials \
  --from-literal=access_key=KEY --from-literal=secret_key=SECRET
```
→ entrypoint.sh が `/etc/passwd-s3fs` (600, 形式: `KEY:SECRET`) として書き出す

### GitHub Actions

main ブランチへの push で自動ビルド・ghcr.io へプッシュ:
- `ghcr.io/tak-lab55/fuse_k8s-sshfs:latest`
- `ghcr.io/tak-lab55/fuse_k8s-s3fs:latest`

タグは `latest` と `YYYYMMDD-<sha>` の2種類。

### プライベートリポジトリでのイメージ取得

```bash
# ghcr.io ログイン
echo <PAT> | docker login ghcr.io -u <GITHUB_USERNAME> --password-stdin

# Kubernetes の imagePullSecret 作成
kubectl create secret docker-registry ghcr-secret \
  --docker-server=ghcr.io \
  --docker-username=<GITHUB_USERNAME> \
  --docker-password=<PAT>
```

`deploy-registry.yaml` の `imagePullSecrets` コメントを外すと有効になる。

## 新しい FUSE 実装を追加する場合のパターン

既存の `sshfs/` または `s3fs/` を参考に以下を作成:
1. `<fs>/Dockerfile` — 同じマルチステージビルド構成、最終ステージで FUSE ツールを差し替え
2. `<fs>/entrypoint.sh` — `touch /dev/fuse` を含む起動スクリプト
3. `<fs>/deploy-kind.yaml` / `deploy-registry.yaml` — CSI エフェメラルボリューム設定を含む Pod 定義
4. `.github/workflows/docker-image.yml` の matrix に追加
