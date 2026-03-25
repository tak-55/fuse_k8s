# Kubernetes FUSE マウント - fuse-csi-driver 実装

Kubernetes Pod 内で FUSE ファイルシステム（sshfs・s3fs）を PSS restricted 環境でマウントするための実装です。

## アーキテクチャ概要

このリポジトリは2つのアーキテクチャを収録しています:

| | 旧アーキテクチャ（sidecar + meta-fuse-csi-plugin） | 新アーキテクチャ（fuse-csi-driver + fd-passing） |
|--|--|--|
| ブランチ | main | feature/fuse-csi-driver-fd-passing |
| PSS restricted | ❌ 不可（privileged 必要） | ✅ 対応 |
| ユーザー Pod サイドカー | privileged: true 必須 | 非特権サイドカー |
| 仕組み | fusermount3-proxy → CSI（CAP_SYS_ADMIN） | CSI が /dev/fuse を open+mount → fd を UDS 経由でサイドカーに渡す |
| 対象用途 | 開発環境・レガシー | Capsule + Kyverno マルチテナント環境 |

### 新アーキテクチャ（fd-passing）の動作原理

```
[User Pod - PSS restricted / hostUsers: false]
  containers:
    app              ← /data にアクセス（uid=1000）
    sshfs-sidecar    ← 非特権、fusermount3-stub + libfuse3 でマウント処理

[Node]
  /var/lib/kubelet/.../volumes/
    ↑ kubelet が bind-mount で Pod の /data に伝播

[CSI DaemonSet (fuse-csi-system, privileged: true)]
  NodePublishVolume:
    1. open("/dev/fuse")    → fusefd を取得
    2. mount(2)             → targetPath に FUSE マウント
    3. UDS 経由で fusefd + 接続パラメータをサイドカーへ送信

  sshfs-sidecar (receiver):
    4. fusefd + 認証情報を受信
    5. FUSE_PREOPEN_FD 環境変数で fusefd を sshfs/s3fs に渡す
    6. touch /dev/fuse → libfuse が fusermount3 経由にフォールバック
    7. fusermount3-stub が fusefd を libfuse に返す
    8. sshfs/s3fs が FUSE イベントループを開始
```

### ポイント: なぜ非特権で動くのか

| 処理 | 担当 | 権限 |
|------|------|------|
| `/dev/fuse` を open | CSI DaemonSet | privileged |
| `mount(2)` システムコール | CSI DaemonSet | privileged |
| sshfs/s3fs のファイルシステム処理 | サイドカー | **非特権** |

CSI DaemonSet がマウントポイントの準備を行うため、サイドカーは特権なしで動作できます。

## サポートするファイルシステム

| ファイルシステム | サイドカーイメージ | プロトコル |
|----------------|-----------------|-----------|
| sshfs | sshfs-sidecar | SSH / SFTP |
| s3fs | s3fs-sidecar | S3 API（MinIO、Ceph、AWS S3 等） |

## ディレクトリ構成

```
fuse_k8s/
├── fuse-csi-driver/            # CSI ドライバー（Go 実装）
│   ├── cmd/                    # エントリーポイント
│   ├── pkg/driver/             # NodePublishVolume・fd-passing 実装
│   └── Dockerfile
├── sshfs-sidecar/              # sshfs サイドカー（fd-passing 用）
│   ├── receiver/               # fd 受信 + sshfs 起動
│   ├── fusermount3-stub/       # libfuse3 をフックして fusefd を渡す stub
│   └── Dockerfile
├── s3fs-sidecar/               # s3fs サイドカー（fd-passing 用）
│   ├── receiver/               # fd 受信 + s3fs 起動
│   ├── fusermount3-stub/       # 同上
│   └── Dockerfile
├── csi/                        # Kubernetes マニフェスト
│   ├── fuse-csi-driver.yaml              # Namespace + CSIDriver リソース
│   ├── fuse-csi-driver-daemonset.yaml    # kind / devcontainer 用
│   ├── fuse-csi-driver-daemonset-prod.yaml  # 本番用（標準 kubelet パス）
│   └── fuse-csi-driver-daemonset-k3s.yaml   # k3s 用
├── sshfs/                      # sshfs ユーザー Pod マニフェスト
│   ├── deploy-kind-fdpass.yaml      # fd-passing 用（kind）
│   └── deploy-kind-no-sidecar.yaml  # サイドカーなし用
├── s3fs/                       # s3fs ユーザー Pod マニフェスト
│   ├── deploy-kind-fdpass.yaml
│   └── deploy-kind-no-sidecar.yaml
├── policy/                     # Capsule Tenant + Kyverno ポリシー
│   ├── capsule-tenant-example.yaml
│   ├── kyverno-force-userns.yaml
│   └── kyverno-force-securecontext.yaml
└── docs/                       # 各種手順書
    ├── guide-kind-setup-and-test.md   # kind ローカル構築手順
    ├── guide-k8s-setup-and-test.md    # k3s 本番構築手順
    ├── guide-admin-operations.md      # 管理者運用手順
    └── guide-user-operations.md       # ユーザー利用手順
```

## CSI DaemonSet マニフェストの選択

| 環境 | マニフェスト | kubelet パス |
|------|------------|-------------|
| kind / devcontainer | `csi/fuse-csi-driver-daemonset.yaml` | `/var/lib/kubelet` |
| kubeadm / RKE2 | `csi/fuse-csi-driver-daemonset-prod.yaml` | `/var/lib/kubelet` |
| k3s | `csi/fuse-csi-driver-daemonset-k3s.yaml` | `/var/lib/rancher/k3s/agent/kubelet` |

## クイックスタート（kind + fd-passing）

### 1. kind クラスター作成

```bash
# .devcontainer/kind-config.yaml には /dev/fuse の extraMounts が含まれている（必須）
kind create cluster --name fuse-dev --config .devcontainer/kind-config.yaml
```

> **macOS + Docker Desktop での制約**: Kyverno が注入する `hostUsers: false` は macOS Docker Desktop
> 上の kind では非対応です（Linux ネイティブ上の kind または k3s では動作します）。
> macOS では FUSE マウント機能の確認のみ可能で、PSS restricted + uid=1000 の完全テストには
> Linux 環境が必要です。

### 2. イメージのビルドと kind へのロード

```bash
# CSI ドライバー
docker build -t fuse-csi-driver:latest ./fuse-csi-driver/
kind load docker-image fuse-csi-driver:latest --name fuse-dev

# サイドカー（fd-passing 用）
docker build -t sshfs-sidecar:latest ./sshfs-sidecar/
docker build -t s3fs-sidecar:latest ./s3fs-sidecar/
kind load docker-image sshfs-sidecar:latest --name fuse-dev
kind load docker-image s3fs-sidecar:latest --name fuse-dev
```

### 3. CSI ドライバーのデプロイ

```bash
kubectl apply -f csi/fuse-csi-driver.yaml
kubectl apply -f csi/fuse-csi-driver-daemonset.yaml
kubectl get pods -n fuse-csi-system
```

### 4. sshfs Pod のデプロイ

```bash
# SSH 秘密鍵を Secret に登録
# 注意: --from-file を使うこと（--from-literal は改行が失われ認証失敗する）
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/sshfs_key

# Pod マニフェストを編集（host / user / remotePath を設定）してデプロイ
cp sshfs/deploy-kind-fdpass.yaml /tmp/sshfs-test.yaml
kubectl apply -f /tmp/sshfs-test.yaml

# 確認
kubectl exec sshfs-fdpass-example -c app -- ls -la /data
```

### 5. s3fs Pod のデプロイ

```bash
# S3 認証情報を Secret に登録
kubectl create secret generic s3-credentials \
  --from-literal=access_key=<ACCESS_KEY> \
  --from-literal=secret_key=<SECRET_KEY>

# Pod マニフェストを編集（bucket / endpoint を設定）してデプロイ
cp s3fs/deploy-kind-fdpass.yaml /tmp/s3fs-test.yaml
kubectl apply -f /tmp/s3fs-test.yaml
```

## 詳細手順書

| 環境 | ドキュメント |
|------|------------|
| kind ローカル構築 | [docs/guide-kind-setup-and-test.md](docs/guide-kind-setup-and-test.md) |
| k3s 本番構築 | [docs/guide-k8s-setup-and-test.md](docs/guide-k8s-setup-and-test.md) |
| 管理者運用 | [docs/guide-admin-operations.md](docs/guide-admin-operations.md) |
| ユーザー利用方法 | [docs/guide-user-operations.md](docs/guide-user-operations.md) |

## セキュリティ考慮事項

- **CSI DaemonSet のみ privileged**: `fuse-csi-system` namespace は管理者が管理し、ユーザー namespace には privileged Pod が一切不要
- **PSS restricted 完全準拠**: `hostUsers: false`・`runAsNonRoot: true`・`seccompProfile: RuntimeDefault` を Kyverno が自動注入
- **Secret 管理**: SSH 秘密鍵・S3 認証情報はユーザーが各自のテナント namespace に作成。管理者は内容を参照しない
- **SSH 秘密鍵の登録**: 必ず `--from-file` を使用する。`--from-literal` は末尾改行が失われ SSH 認証エラーになる

## 旧アーキテクチャ（meta-fuse-csi-plugin）について

`sshfs/` および `s3fs/` ディレクトリには旧アーキテクチャ（meta-fuse-csi-plugin + fusermount3-proxy）の実装も含まれています。旧アーキテクチャは `privileged: true` のサイドカーが必要であり、PSS restricted には対応していません。main ブランチを参照してください。

## 参考資料

- [libfuse](https://github.com/libfuse/libfuse) - Linux FUSE ライブラリ
- [sshfs](https://github.com/libfuse/sshfs) - SSH Filesystem
- [s3fs-fuse](https://github.com/s3fs-fuse/s3fs-fuse) - S3 Filesystem FUSE
- [Capsule](https://capsule.clastix.io/) - Kubernetes マルチテナント
- [Kyverno](https://kyverno.io/) - Kubernetes ポリシーエンジン
- [Container Storage Interface Spec](https://github.com/container-storage-interface/spec) - CSI 仕様
