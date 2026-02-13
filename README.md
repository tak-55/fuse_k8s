# meta-fuse-csi-plugin / sshfs 汎用マニフェスト

## 概要

[meta-fuse-csi-plugin](https://github.com/pfnet-research/meta-fuse-csi-plugin) を使って、
ユーザー Pod から sshfs リモートファイルシステムをマウントするためのマニフェスト集です。

fusermount3-proxy を介して CSI DaemonSet にマウント操作を委譲することで、
ユーザー Pod 側で直接 `/dev/fuse` を `open()` する必要がなくなります。

> **注意**: 現在の `sshfs/deploy.yaml` では sshfs-proxy コンテナに `privileged: true` を設定しています。
> 環境に応じて securityContext を調整してください。

```
┌──────────────────────────────────────────────────────┐
│  User Pod                                            │
│  ┌──────────────────┐   ┌────────────────────────┐  │
│  │ sshfs-proxy      │   │ app container          │  │
│  │ (sidecar)        │   │ /data → sshfs マウント  │  │
│  │  sshfs           │   │                        │  │
│  │  fusermount3     │   │                        │  │
│  │  ─proxy─         │   │                        │  │
│  └────────┬─────────┘   └────────────────────────┘  │
│           │ UDS (Unix Domain Socket)                 │
└───────────┼──────────────────────────────────────────┘
            │
┌───────────▼──────────────────────────────────────────┐
│  CSI DaemonSet Pod (mfcp-system namespace)           │
│  CAP_SYS_ADMIN あり (privileged: true)               │
│  → open("/dev/fuse") + mount() を代行               │
└──────────────────────────────────────────────────────┘
```

## ファイル構成

```
.
├── deploy/
│   ├── csi-driver.yaml            # CSIDriver リソース + Namespace (mfcp-system)
│   └── csi-driver-daemonset.yaml  # DaemonSet + ServiceAccount + RBAC (各ノードに1Pod)
└── sshfs/
    ├── Dockerfile                 # sshfs サイドカーイメージ (マルチステージビルド)
    ├── entrypoint.sh              # sshfs 起動スクリプト (touch /dev/fuse + sshfs -f)
    └── deploy.yaml                # Pod マニフェスト (K8s v1.29+ サイドカー対応)
```

## クイックスタート

### 1. CSI ドライバのデプロイ (クラスター管理者が1回のみ実施)

```bash
kubectl apply -f deploy/csi-driver.yaml
kubectl apply -f deploy/csi-driver-daemonset.yaml

# DaemonSet の準備確認
kubectl get ds -n mfcp-system
```

CSI ドライバイメージ: `ghcr.io/pfnet-research/meta-fuse-csi-plugin/meta-fuse-csi-plugin:v0.2.2`

### 2. sshfs サイドカーイメージのビルド & プッシュ

Dockerfile はマルチステージビルドで、Stage 1 で `fusermount3-proxy` を Go でビルドし、
Stage 2 で Ubuntu 22.04 ベースの sshfs イメージに組み込みます。

```bash
# Dockerfile 内の git clone が meta-fuse-csi-plugin リポジトリを自動取得するため、
# ソースの事前配置は不要

cd sshfs/
docker build -t sshfs-proxy:latest .
```

**レジストリにプッシュする場合:**

```bash
docker tag sshfs-proxy:latest your-registry/sshfs-proxy:latest
docker push your-registry/sshfs-proxy:latest
```

**kind を使う場合:**

```bash
kind load docker-image sshfs-proxy:latest
```

> kind の場合、deploy.yaml の `imagePullPolicy` が `Never` または `IfNotPresent` であることを確認してください。

### 3. SSH 鍵の準備

事前に SSH 鍵ペアを生成し、公開鍵を接続先の SSH サーバーに配置してください。

```bash
# 鍵ペアの生成 (未作成の場合)
ssh-keygen -t ed25519 -f ~/.ssh/sshfs_key -N ""

# 公開鍵を SSH サーバーに配置
ssh-copy-id -i ~/.ssh/sshfs_key.pub user@your-ssh-server
```

秘密鍵を Kubernetes Secret として登録します。
Secret 名 `ssh-key`、キー `private_key` を使用し、Pod 内では `/secrets/ssh/private_key` にマウントされます。

```bash
kubectl create secret generic ssh-key \
  --from-file=private_key=${HOME}/.ssh/sshfs_key
```

### 4. SSH接続パラメータを編集

`sshfs/deploy.yaml` の Pod spec 内で sshfs-proxy コンテナの `env` に直接指定されている値を環境に合わせて編集してください。

```yaml
env:
  - name: SSHFS_HOST
    value: "192.168.72.27"         # ← SSH接続先 (IP/ホスト名)
  - name: SSHFS_USER
    value: "demouser"              # ← SSHユーザー
  - name: SSHFS_REMOTE_PATH
    value: "/home/demouser"        # ← リモートパス
  - name: SSHFS_PORT
    value: "22"                    # ← SSHポート
  - name: USE_LOCAL_SSHD
    value: "false"                 # ← "true" でコンテナ内 sshd を起動 (デモ用)
```

### 5. デプロイ

```bash
kubectl apply -f sshfs/deploy.yaml
```

### 6. 動作確認

```bash
# Pod ステータス確認
kubectl get pod sshfs-example

# sshfs マウントが正常に完了しているか確認
kubectl exec sshfs-example -c app -- ls /data

# マウント情報の確認
kubectl exec sshfs-example -c app -- mount | grep fuse
```

## 環境変数リファレンス (sshfs-proxy コンテナ)

| 変数名               | 必須 | デフォルト      | 説明                                 |
|---------------------|:----:|-----------------|--------------------------------------|
| `SSHFS_HOST`        | ✅   | `localhost`     | SSH接続先ホスト (IP/ホスト名)         |
| `SSHFS_USER`        | ✅   | `root`          | SSHユーザー名                         |
| `SSHFS_REMOTE_PATH` | -    | `/root/sshfs-example` | リモートマウントパス           |
| `SSHFS_PORT`        | -    | `22`            | SSHポート番号                         |
| `SSHFS_MOUNT_POINT` | -    | `/tmp`          | コンテナ内マウントポイント (deploy.yaml では `/mnt/sshfs` に上書き) |
| `USE_LOCAL_SSHD`    | -    | `false`         | `true` でコンテナ内 sshd を起動 (デモ用) |
| `SSH_PRIVATE_KEY`   | -    | -               | SSH秘密鍵の内容 (環境変数経由で注入する場合。entrypoint.sh が `/root/.ssh/private_key` に書き出す) |
| `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` | ✅ | - | CSI DaemonSet との UDS ソケットフルパス。CSI ボリュームの `socketDir/socketName` と一致させること (例: `/var/lib/mfcp/uds/mfcp.sock`) |

## CSI ボリューム volumeAttributes

Pod の `volumes[].csi.volumeAttributes` で指定する属性です。

| 属性名                    | 説明                                           | 例                       |
|--------------------------|------------------------------------------------|--------------------------|
| `socketDir`              | UDS ソケットのディレクトリパス                    | `/var/lib/mfcp/uds`     |
| `socketName`             | UDS ソケットファイル名                           | `mfcp.sock`             |
| `fdPassingEmptyDirName`  | UDS 用 emptyDir ボリューム名 (deploy.yaml で使用) | `fuse-socket-dir`       |
| `fdPassingSocketName`    | FD パッシング用ソケット名 (deploy.yaml で使用)    | `mfcp.sock`             |

## `touch /dev/fuse` が必要な理由

`entrypoint.sh` 内で `touch /dev/fuse` を実行しています。
libfuse3 はマウント時にまず `/dev/fuse` を直接 `open()` しようとします。コンテナ内では権限がないため失敗しますが、**`/dev/fuse` ファイルが存在するだけで**、libfuse は代替パスである `fusermount3` 経由での処理に切り替わります。この `fusermount3` が `fusermount3-proxy` に差し替えられているため (Dockerfile 参照)、結果として CSI DaemonSet へ処理が委譲されます。

```
entrypoint.sh
  └─ touch /dev/fuse
  └─ sshfs ... -f (フォアグラウンド)
       └─ libfuse3
            └─ open("/dev/fuse")  → 失敗 (権限なし)
            └─ exec fusermount3   → fusermount3-proxy が代わりに応答
                 └─ UDS経由 ────> CSI DaemonSet
                                     └─ open("/dev/fuse") + mount()  (CAP_SYS_ADMIN あり)
```

## 前提条件

- Kubernetes v1.29 以上 (SidecarContainers 機能を使用するため)