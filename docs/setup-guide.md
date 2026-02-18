# fuse_k8s セットアップガイド

> **関連文書**:
> - [meta-fuse-csi-plugin 調査レポート](./meta-fuse-csi-plugin-report.md)
> - [実装変更報告書](./implementation-changes.md)

---

## 1. 環境設定

### 前提条件

| 項目 | 要件 |
|------|------|
| Kubernetes | **v1.29+**（SidecarContainers 機能が必須） |
| OS | Linux ノード (`kubernetes.io/os=linux`) |
| ノード権限 | CSI Driver Pod 用の `CAP_SYS_ADMIN` をクラスター管理者が許可 |
| ビルドツール | Docker または Podman（サイドカーイメージのビルド用） |
| ローカル検証 | kind (Kubernetes in Docker) で動作確認可能 |

### サポートする FUSE 実装

| FUSE 実装 | アプローチ | 用途 |
|-----------|-----------|------|
| [sshfs](https://github.com/libfuse/sshfs) | fusermount3-proxy | SSH プロトコルでリモートファイルシステムをマウント |
| [s3fs](https://github.com/s3fs-fuse/s3fs-fuse) | fusermount3-proxy | S3 互換ストレージ（MinIO、Ceph、AWS S3 等）をマウント |

### ローカル検証環境の準備（kind）

```bash
# kind クラスターの作成
kind create cluster --name fuse-dev

# kubectl コンテキストの確認
kubectl cluster-info --context kind-fuse-dev
```

---

## 2. インストール方法

### ステップ 1: リポジトリのクローン

```bash
git clone https://github.com/<your-org>/fuse_k8s.git
cd fuse_k8s
```

### ステップ 2: CSI ドライバーのデプロイ

すべての FUSE 実装で共通の CSI ドライバーをデプロイします（クラスターにつき1回のみ）。

```bash
kubectl apply -f csi/csi-driver.yaml
kubectl apply -f csi/csi-driver-daemonset.yaml
```

適用後の出力例：

```
namespace/mfcp-system created
csidriver.storage.k8s.io/meta-fuse-csi-plugin.csi.storage.pfn.io created
daemonset.apps/meta-fuse-csi-plugin created
```

### ステップ 3: デプロイの確認

```bash
# DaemonSet の状態確認
kubectl get ds -n mfcp-system

# Pod の起動確認
kubectl get pods -n mfcp-system
```

正常な出力例：

```
NAME                   DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR            AGE
meta-fuse-csi-plugin   1         1         1       1            1           kubernetes.io/os=linux   1m
```

### ステップ 4: サイドカーイメージのビルド

使用する FUSE 実装のイメージをビルドします。

```bash
# sshfs
docker build -t sshfs-proxy:latest ./sshfs/

# s3fs
docker build -t s3fs-proxy:latest ./s3fs/
```

kind 環境の場合はイメージをロードします。

```bash
kind load docker-image sshfs-proxy:latest --name fuse-dev
kind load docker-image s3fs-proxy:latest --name fuse-dev
```

> **注意**: kind 環境では `deploy-kind.yaml`（`imagePullPolicy: Never`）を使用してください。

---

## 3. 利用方法

### sshfs（SSH リモートファイルシステム）

#### 3.1.1 認証情報の準備

```bash
# SSH 鍵ペアの生成（未作成の場合）
ssh-keygen -t ed25519 -f ~/.ssh/sshfs_key -N ""

# 公開鍵を SSH サーバーに配置
ssh-copy-id -i ~/.ssh/sshfs_key.pub user@your-ssh-server

# 秘密鍵を Kubernetes Secret として登録
kubectl create secret generic ssh-key \
  --from-file=private_key=${HOME}/.ssh/sshfs_key
```

#### 3.1.2 マニフェストの編集

マニフェストの環境変数を編集します。kind 環境では `sshfs/deploy-kind.yaml`、レジストリ利用時は `sshfs/deploy-registry.yaml` を使用してください。

| 環境変数 | 説明 | 例 |
|---------|------|-----|
| `SSHFS_HOST` | SSH 接続先ホスト | `192.168.72.27` |
| `SSHFS_USER` | SSH ユーザー名 | `demouser` |
| `SSHFS_REMOTE_PATH` | リモートパス | `/home/demouser` |
| `SSHFS_PORT` | SSH ポート番号 | `22` |

#### 3.1.3 デプロイと動作確認

```bash
# デプロイ（kind 環境）
kubectl apply -f sshfs/deploy-kind.yaml

# デプロイ（レジストリ利用）
kubectl apply -f sshfs/deploy-registry.yaml

# Pod の起動確認
kubectl get pod sshfs-example

# マウント確認
kubectl exec sshfs-example -c app -- mount | grep fuse.sshfs

# ファイル一覧確認
kubectl exec sshfs-example -c app -- ls -la /data
```

---

### s3fs（S3 互換ストレージ）

#### 3.2.1 認証情報の準備

```bash
kubectl create secret generic s3-credentials \
  --from-literal=access_key=YOUR_ACCESS_KEY \
  --from-literal=secret_key=YOUR_SECRET_KEY
```

#### 3.2.2 マニフェストの編集

マニフェストの環境変数を編集します。kind 環境では `s3fs/deploy-kind.yaml`、レジストリ利用時は `s3fs/deploy-registry.yaml` を使用してください。

| 環境変数 | 説明 | 例 |
|---------|------|-----|
| `S3FS_BUCKET` | S3 バケット名 | `my-bucket` |
| `S3FS_ENDPOINT` | S3 エンドポイント URL | `http://minio.default:9000` |
| `S3FS_REGION` | リージョン | `us-east-1` |

#### 3.2.3 デプロイと動作確認

```bash
# デプロイ（kind 環境）
kubectl apply -f s3fs/deploy-kind.yaml

# デプロイ（レジストリ利用）
kubectl apply -f s3fs/deploy-registry.yaml

# Pod の起動確認
kubectl get pod s3fs-example

# マウント確認
kubectl exec s3fs-example -c app -- mount | grep fuse.s3fs

# ファイル一覧確認
kubectl exec s3fs-example -c app -- ls -la /data
```

---

## 補足: 環境変数リファレンス

### sshfs サイドカー

| 変数名 | 必須 | デフォルト | 説明 |
|--------|------|-----------|------|
| `SSHFS_HOST` | ✓ | `localhost` | SSH 接続先ホスト |
| `SSHFS_USER` | ✓ | `root` | SSH ユーザー名 |
| `SSHFS_REMOTE_PATH` | | `/root/sshfs-example` | リモートマウントパス |
| `SSHFS_PORT` | | `22` | SSH ポート番号 |
| `SSHFS_MOUNT_POINT` | | `/tmp` | マウント先パス |
| `USE_LOCAL_SSHD` | | `false` | `true` でコンテナ内 sshd を起動 |
| `SSH_PRIVATE_KEY` | | - | SSH 秘密鍵（環境変数経由で注入する場合） |
| `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` | ✓ | `/var/lib/mfcp/uds/mfcp.sock` | UDS ソケットパス |

### s3fs サイドカー

| 変数名 | 必須 | デフォルト | 説明 |
|--------|------|-----------|------|
| `S3FS_BUCKET` | ✓ | - | S3 バケット名 |
| `S3FS_ENDPOINT` | ✓ | - | S3 エンドポイント URL |
| `S3FS_REGION` | | `us-east-1` | S3 リージョン |
| `S3FS_MOUNT_POINT` | | `/mnt/s3fs` | マウント先パス |
| `AWS_ACCESS_KEY_ID` | ✓ | - | アクセスキー（Secret から注入） |
| `AWS_SECRET_ACCESS_KEY` | ✓ | - | シークレットキー（Secret から注入） |
| `S3FS_OPTS` | | (空) | 追加の s3fs オプション |
| `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` | ✓ | `/var/lib/mfcp/uds/mfcp.sock` | UDS ソケットパス |
