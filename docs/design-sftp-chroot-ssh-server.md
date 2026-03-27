# SFTP ChrootDirectory 設計書

**対象機能**: fuse-csi-driver / sshfs-sidecar の SSH バックエンドにおける SFTP + chroot 構成
**ブランチ**: `feature/fuse-csi-driver-fd-passing`
**作成日**: 2026-03-26

---

## 1. 背景と目的

### 1.1 背景

sshfs は SSH プロトコル上の **SFTP サブシステム**を利用してリモートファイルシステムをマウントする。SSH サーバーをシェルアクセスなしの SFTP 専用サーバーとして構成し、さらに `ChrootDirectory` で各ユーザーのアクセス範囲を制限することは、本番運用における標準的なセキュリティ対策である。

### 1.2 目的

- fuse-csi-driver が接続する SSH サーバーを SFTP + chroot 構成で設計・検証する
- シェルアクセスを排除し、ユーザーが chroot 外のファイルシステムを参照できないことを保証する
- テスト環境（kind）での再現可能な SSH サーバーセットアップ手順を確立する

---

## 2. 要件

| ID | 要件 |
|----|------|
| S-01 | SSH ログインユーザーはシェルを持たない（SFTP 専用） |
| S-02 | 各ユーザーは `ChrootDirectory` 内のみアクセス可能 |
| S-03 | chroot ディレクトリは root 所有・755（OpenSSH の要件） |
| S-04 | sshfs から chroot 内のサブディレクトリへの読み書きが可能 |
| S-05 | SSH 秘密鍵認証のみ許可（パスワード認証無効） |

---

## 3. アーキテクチャ

### 3.1 全体フロー

```
[User Pod - test-tenant-ns]
  containers:
    app (busybox, UID 1000)
      /data ← FUSE bind-mount

    sshfs-sidecar (UID 1000)
      FUSE_PREOPEN_FD=<fd> で sshfs 起動
      sshfs → SFTP → SSH Server Pod (port 2222)
                         ChrootDirectory /chroot/testuser
                         /chroot/testuser/data/ ← remotePath = /data

[CSI DaemonSet - fuse-csi-system]
  NodePublishVolume:
    open(/dev/fuse) + mount()
    UDS server → fusefd + SSH 秘密鍵 → sshfs-sidecar

[SSH Server Pod - default ns]
  sshd (port 2222)
  /chroot/testuser/        root:root  755  ← chroot root（書き込み不可）
  /chroot/testuser/data/   testuser   755  ← sshfs がマウントするパス
```

### 3.2 ChrootDirectory の制約

OpenSSH の `ChrootDirectory` には以下のカーネル要件がある:

| 項目 | 条件 |
|------|------|
| chroot ディレクトリの所有者 | **root** でなければならない |
| chroot ディレクトリのパーミッション | **root 以外が書き込めない**（755 または 711） |
| chroot ディレクトリのサブパス全て | 同様に root 所有・755 以下 |

これは OpenSSH が chroot 後のジェイルが改ざんされないことを保証するための要件である。

### 3.3 remotePath とパスの対応

```
sshfs から見えるパス   実ファイルシステム上のパス
/                     /chroot/testuser/
/data                 /chroot/testuser/data/
/data/test.txt        /chroot/testuser/data/test.txt
```

fuse-csi-driver の volumeAttributes:
```yaml
remotePath: "/data"   # chroot 内のパスを指定
```

---

## 4. SSH サーバー構成

### 4.1 sshd_config

```
Port 2222
HostKey /etc/ssh/host_keys/ssh_host_ed25519_key

# 認証
PubkeyAuthentication yes
PasswordAuthentication no
PermitRootLogin no
AuthorizedKeysFile /chroot/%u/.ssh/authorized_keys

# SFTP 専用サブシステム（内蔵 SFTP サーバー使用）
Subsystem sftp internal-sftp

# testuser を SFTP + chroot に制限
Match User testuser
    ChrootDirectory /chroot/%u
    ForceCommand internal-sftp
    AllowTcpForwarding no
    X11Forwarding no
    PermitTTY no
```

#### internal-sftp を使う理由

| 項目 | external sftp-server | internal-sftp |
|------|---------------------|---------------|
| chroot との互換性 | `/dev/log` 等のソケットが必要 | 不要（カーネル内で完結） |
| 設定の簡潔さ | デバイスの bind mount が必要 | 不要 |
| セキュリティ | OpenSSH とは別プロセス | OpenSSH プロセス内で動作 |

`internal-sftp` は chroot 環境で `/dev/log` や `/proc` が不要なため、最小権限の chroot 構成に適している。

### 4.2 chroot ディレクトリ構造

```
/chroot/
└── testuser/                    root:root  755  ← ChrootDirectory
    ├── .ssh/
    │   └── authorized_keys      root:root  644  ← 公開鍵
    └── data/                    testuser:testuser  755  ← sshfs マウント先
```

#### 初期化スクリプト（init container）

```sh
# chroot root: root 所有・書き込み不可（OpenSSH の要件）
mkdir -p /chroot/testuser
chown root:root /chroot/testuser
chmod 755 /chroot/testuser

# authorized_keys: chroot 内に配置
mkdir -p /chroot/testuser/.ssh
chmod 700 /chroot/testuser/.ssh
cp /authorized-keys/authorized_keys /chroot/testuser/.ssh/authorized_keys
chown -R root:root /chroot/testuser/.ssh
chmod 644 /chroot/testuser/.ssh/authorized_keys

# data ディレクトリ: testuser が読み書き可能
mkdir -p /chroot/testuser/data
chown 1000:1000 /chroot/testuser/data
chmod 755 /chroot/testuser/data

# SSH ホスト鍵の生成
ssh-keygen -t ed25519 -f /etc/ssh/host_keys/ssh_host_ed25519_key -N ""
```

---

## 5. fuse-csi-driver 側の設定

### 5.1 volumeAttributes

```yaml
volumeAttributes:
  type: sshfs
  host: "ssh-server.default.svc.cluster.local"
  user: "testuser"
  remotePath: "/data"       # chroot 内のパス（/ = chroot root）
  port: "22"
  strictHostKeyCheck: "false"   # テスト環境：ホスト鍵検証をスキップ
```

### 5.2 SSH 秘密鍵 Secret

```bash
# 注意: --from-file を使うこと。--from-literal はシェル展開で末尾改行を除去するため
#        OpenSSH が "error in libcrypto" で鍵を読み込めない。
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/sshfs-test-key \
  -n test-tenant-ns
```

---

## 6. セキュリティ考慮事項

### 6.1 chroot による隔離

| 脅威 | 対策 |
|------|------|
| ユーザーが `/etc/passwd` 等を読む | chroot により `/chroot/testuser` 外へのアクセス不可 |
| ユーザーがシェルを起動する | `ForceCommand internal-sftp` によりシェル起動不可 |
| ユーザーが chroot 外にエスケープする | chroot root を root 所有・755 にすることで chroot 破りを防止 |
| パスワード総当たり | `PasswordAuthentication no` で無効化 |

### 6.2 CSI driver 側のセキュリティ

- SSH 秘密鍵は Kubernetes Secret として管理（`nodePublishSecretRef`）
- 秘密鍵は CSI driver が一時ファイルに書き出し（`CreateTemp`）、プロセス終了後に削除
- 秘密鍵は emptyDir には書かれない（UDS 経由で直接 sshfs-sidecar に渡す）

### 6.3 sshfs マウントオプション

```
-o allow_other    # FUSE: UID 0 以外のプロセス（uid 1000）からのアクセスを許可
-o umask=000      # デフォルト umask を 000 に（CSI driver が user_id=0 でマウントするため）
-o IdentityFile=<tmpfile>   # 秘密鍵の一時ファイル
-o StrictHostKeyChecking=accept-new   # 本番: 初回接続時に鍵を記録
```

---

## 7. 既知の制約

### 7.1 FUSE CSI ボリュームと hostUsers: false の非互換

| 項目 | 詳細 |
|------|------|
| 問題 | `/dev/fuse` 経由の標準 FUSE は `MOUNT_ATTR_IDMAP` をサポートしない |
| 影響 | `hostUsers: false` 環境では runc が `EINVAL` で失敗する |
| 対処 | Kyverno ポリシー `force-userns-for-tenant` に preconditions を追加し、FUSE CSI ボリュームを持つ Pod は `hostUsers: false` の注入をスキップ |
| ファイル | `policy/kyverno-force-userns.yaml` |

virtiofs（VM ベース FUSE）は `MOUNT_ATTR_IDMAP` に対応しているが、`/dev/fuse` 経由の FUSE は kernel 6.8 時点でも非対応。

### 7.2 Secret の作成方法

SSH 秘密鍵は `--from-file` で作成すること。`--from-literal` はシェル展開で末尾改行を除去するため、OpenSSH が `error in libcrypto` で鍵を読み込めない。

```bash
# NG: 末尾改行が消える
kubectl create secret generic ssh-key \
  --from-literal=private_key="$(cat ~/.ssh/key)"

# OK: ファイルの内容をそのまま格納
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/key \
  -n <namespace>
```

### 7.3 sshfs-sidecar と app コンテナの起動順序

FUSE CSI ドライバが `NodePublishVolume` で FUSE マウントを作成した時点では、FUSE デーモン（sshfs）が未起動のため、bind mount 時に runc が 2分タイムアウトする場合がある。

対処: sshfs-sidecar を **sidecar initContainer** として定義し、`readinessProbe` で sshfs 起動後に app コンテナを起動する。

```yaml
initContainers:
  - name: sshfs-sidecar
    restartPolicy: Always   # ← sidecar として動作（Kubernetes 1.29+）
    readinessProbe:
      exec:
        command: ["test", "-f", "/fuse-fd/ready"]
```

## 8. テスト環境（kind）セットアップ

### 7.1 SSH サーバー Pod

`csi/test-ssh-server.yaml` として管理する（→ セクション 8 参照）。

デプロイ手順:
```bash
# 1. SSH キーペア生成
ssh-keygen -t ed25519 -f /tmp/test-sshkey -N ""

# 2. 公開鍵を ConfigMap に登録してサーバーをデプロイ
kubectl create configmap sshd-authorized-keys \
  --from-file=authorized_keys=/tmp/test-sshkey.pub \
  -n default
kubectl apply -f csi/test-ssh-server.yaml

# 3. 秘密鍵を テナント namespace の Secret に登録
kubectl create secret generic ssh-key \
  --from-literal=private_key="$(cat /tmp/test-sshkey)" \
  -n test-tenant-ns

# 4. テスト Pod デプロイ
kubectl apply -f sshfs/deploy-kind-fdpass.yaml -n test-tenant-ns
```

### 7.2 期待される動作確認

```bash
# CSI driver ログ
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver

# sshfs-sidecar ログ（fusefd受信 → sshfs起動を確認）
kubectl logs <pod> -n test-tenant-ns -c sshfs-sidecar

# ファイルアクセステスト
kubectl exec <pod> -n test-tenant-ns -c app -- ls /data
kubectl exec <pod> -n test-tenant-ns -c app -- sh -c 'echo test > /data/hello.txt && cat /data/hello.txt'
```

---

## 8. 関連ファイル

| ファイル | 説明 |
|----------|------|
| `csi/test-ssh-server.yaml` | テスト用 SSH サーバー（SFTP + chroot）の K8s マニフェスト |
| `sshfs/deploy-kind-fdpass.yaml` | fd-passing + sshfs-sidecar のテスト Pod |
| `sshfs/deploy-kind-fdpass.yaml` | fd-passing + sshfs-sidecar のテスト Pod（kind 用） |
| `sshfs-sidecar/receiver/main.go` | sshfs-sidecar のエントリポイント |
| `sshfs-sidecar/fusermount3-stub/main.go` | libfuse の fusermount3 インターセプト |
| `fuse-csi-driver/pkg/driver/fdpassing.go` | fd-passing の CSI driver 側実装 |
