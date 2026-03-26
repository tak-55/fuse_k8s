# FUSE CSI Driver 設計書

**対象機能**: PSS restricted 環境でのユーザー Pod からの FUSE マウント提供
**ブランチ**: `feature/fuse-csi-driver-pss-restricted`
**作成日**: 2026-03-24

---

## 1. 背景と目的

### 1.1 背景

現在の実装（meta-fuse-csi-plugin + sidecar 方式）では、ユーザー Pod の sidecar コンテナに `privileged: true` が必須となっている。これは `mountPropagation: Bidirectional` が Kubernetes の仕様上 `privileged: true` を要求するためであり、Pod Security Standards (PSS) の `restricted` プロファイルと根本的に相容れない。

Kubernetes クラスターを Capsule + Kyverno によるマルチテナント環境として運用する場合、テナント内の全 namespace に PSS restricted が適用されるため、現行方式ではユーザーが FUSE ファイルシステムを利用できない。

### 1.2 目的

- ユーザー Pod（sidecar・app コンテナとも）を `privileged: false` で動作させる
- Capsule / Kyverno によって強制される PSS restricted / `hostUsers: false` に準拠する
- 10〜50 ユーザーの並列 FUSE マウントを管理者管理の CSI DaemonSet で提供する
- sshfs（SSH リモートファイルシステム）と s3fs（S3 互換ストレージ）の両方に対応する

---

## 2. 要件

### 2.1 機能要件

| ID | 要件 |
|----|------|
| F-01 | sshfs により SSH サーバーのリモートパスをマウントできる |
| F-02 | s3fs により S3 互換ストレージのバケットをマウントできる |
| F-03 | マウント先ディレクトリへの読み書きができる |
| F-04 | 10〜50 ユーザーの並列マウントを同一ノード上で管理できる |
| F-05 | Pod 削除時に FUSE プロセスが正常にクリーンアップされる |

### 2.2 セキュリティ要件

| ID | 要件 |
|----|------|
| S-01 | ユーザー Pod（sidecar・app）は `privileged: false` で動作する |
| S-02 | ユーザー Pod は PSS `restricted` プロファイルに準拠する |
| S-03 | `hostUsers: false`（ユーザー名前空間）が Kyverno によって強制される |
| S-04 | ユーザー Pod の securityContext は Kyverno が自動注入する（ユーザーが書く必要なし） |
| S-05 | 認証情報（SSH 秘密鍵・S3 認証情報）は Kubernetes Secret 経由で渡す |
| S-06 | テナント間のマウント分離が保証される（他テナントのマウントにアクセス不可） |

### 2.3 非機能要件

| ID | 要件 |
|----|------|
| N-01 | Kubernetes 1.30+ で動作する |
| N-02 | Linux カーネル 4.18+ で動作する（user namespace + FUSE 対応） |
| N-03 | CSI DaemonSet の再起動後、ユーザー Pod を再起動することでマウントが復旧する |

---

## 3. アーキテクチャ

### 3.1 全体構成

```
┌──────────────────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                              │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  Capsule / Kyverno (クラスター管理者が管理)                │    │
│  │  - Tenant ごとに namespace を管理                         │    │
│  │  - PSS restricted ラベルを namespace に自動付与           │    │
│  │  - hostUsers: false を全テナント Pod に強制               │    │
│  │  - runAsNonRoot / seccompProfile 等を自動注入             │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
│  ┌───────────────────────┐   ┌──────────────────────────────┐   │
│  │  fuse-csi-system      │   │  テナント namespace          │   │
│  │  (管理者管理)          │   │  (PSS restricted)            │   │
│  │                       │   │                              │   │
│  │  fuse-csi-driver      │   │  User Pod                    │   │
│  │  DaemonSet            │◄──┤  - sshfs/s3fs-sidecar        │   │
│  │  (privileged: true)   │   │    (sidecar initContainer)   │   │
│  │                       │   │    fd-passing で FUSE 提供   │   │
│  │  NodePublishVolume()  │   │  - app コンテナ              │   │
│  │  → /dev/fuse open     │   │    UID: 1000 (Kyverno注入)   │   │
│  │  → mount(2)           │   │    /data ← FUSE マウント      │   │
│  │  → UDS で fd 送信     │   │  - hostUsers: false          │   │
│  └───────────────────────┘   └──────────────────────────────┘   │
│           ↑                                ↑                     │
│    CSI NodePublishVolume           kubelet bind-mount            │
│    (/var/lib/kubelet/pods/...)                                   │
└──────────────────────────────────────────────────────────────────┘
```

### 3.2 現行方式との比較

| 項目 | 現行（sidecar 方式） | 新方式（CSI 内蔵） |
|------|--------------------|--------------------|
| ユーザー Pod sidecar | `privileged: true` 必須 | **sshfs/s3fs-sidecar（非特権）** |
| ユーザー Pod securityContext | 手動設定 | **Kyverno が自動注入** |
| `hostUsers: false` | なし | **Kyverno が自動強制** |
| PSS restricted | ❌ 不可 | ✅ 準拠 |
| `mountPropagation: Bidirectional` | ユーザー Pod に必要 | **CSI DaemonSet のみ** |
| fusermount3-proxy | 必要 | **不要** |
| FUSE 書き込み | 制限あり | **allow_other + umask=000** |
| 認証情報の渡し方 | ConfigMap + Secret（Pod spec）| `nodePublishSecretRef` |
| マルチユーザー設計 | なし | **50 並列対応** |
| テナント分離 | なし | **Capsule Tenant** |

### 3.3 マウント処理フロー

```
ユーザーが Pod を作成
    │
    ▼
Kyverno が Pod spec をミューテーション
  - spec.hostUsers: false を注入
  - runAsNonRoot: true, runAsUser: 1000 を注入
  - allowPrivilegeEscalation: false, capabilities.drop: ALL を注入
    │
    ▼
kubelet が NodePublishVolume を CSI DaemonSet に要求
  - targetPath: /var/lib/kubelet/pods/<uid>/volumes/.../fuse-vol
  - volumeContext: {type, host, remotePath, ...}
  - secrets: {private_key} or {access_key, secret_key}
    │
    ▼
fuse-csi-driver (NodePublishVolume) ← fd-passing 方式
  - open("/dev/fuse") → fusefd 取得
  - mount(fusefd, targetPath, "fuse", ...) でマウントポイント確保
  - emptyDir に params.json（接続パラメータ）を書き出す
  - UDS サーバーを起動（emptyDir/csi.sock）
    │
    ▼
sshfs/s3fs-sidecar（sidecar initContainer）が UDS に接続
  - params.json を読み込み（接続先情報）
  - SCM_RIGHTS で fusefd + 認証情報 JSON を受信
  - sshfs/s3fs を FUSE_PREOPEN_FD=<fusefd> で起動
  - /fuse-fd/ready を書き込み → readinessProbe 通過
    │
    ▼
kubelet が targetPath をユーザー Pod にバインドマウント
  - コンテナの /data として提供
  - allow_other + umask=000 により UID 1000 から読み書き可
    │
    ▼
Pod 削除時
  - NodeUnpublishVolume を呼び出し
  - UDS サーバー停止 → fusefd close → FUSE デーモン終了
  - umount2(targetPath, MNT_DETACH) でアンマウント
  - 一時ファイルを削除
```

---

## 4. コンポーネント設計

### 4.1 CSI Driver (Go)

**ドライバー名**: `fuse.csi.fuse-k8s.io`

**ディレクトリ構成**:

```
fuse-csi-driver/
├── cmd/
│   └── main.go           # エントリーポイント、フラグ解析
├── pkg/
│   └── driver/
│       ├── driver.go     # Driver 型、gRPC サーバー起動
│       ├── identity.go   # CSI Identity サービス
│       ├── node.go       # CSI Node サービス（NodePublishVolume/NodeUnpublishVolume）
│       ├── fdpassing.go  # fd-passing: /dev/fuse open + mount + UDS サーバー
│       ├── sshfs.go      # sshfs マウント処理
│       └── s3fs.go       # s3fs マウント処理
├── go.mod
├── go.sum
└── Dockerfile
```

**実装する CSI RPC**:

| RPC | 説明 |
|-----|------|
| `GetPluginInfo` | ドライバー名・バージョンを返す |
| `GetPluginCapabilities` | Node-only（Controller 不要） |
| `Probe` | ヘルスチェック |
| `NodePublishVolume` | sshfs/s3fs を起動してマウント |
| `NodeUnpublishVolume` | アンマウントとプロセス停止 |
| `NodeGetCapabilities` | Node capability なし |
| `NodeGetInfo` | Node ID を返す |

**プロセス管理**（マルチユーザー対応）:

```go
type Driver struct {
    mounts map[string]*mountInfo  // targetPath → プロセス情報
    mu     sync.RWMutex           // 並列アクセス保護
}

type mountInfo struct {
    fusefd    int        // /dev/fuse ファイルディスクリプタ（close でサイドカー終了）
    stopFn    func()     // UDS サーバー停止関数
    fsType    string     // "sshfs" or "s3fs"
    mountedAt time.Time
}
```

### 4.2 sshfs マウントオプション

| オプション | 値 | 理由 |
|-----------|-----|------|
| `-p` | `${port}` | SSH ポート指定 |
| `-o StrictHostKeyChecking` | `accept-new` (デフォルト) / `no` | ホスト鍵検証 |
| `-o IdentityFile` | 一時ファイルパス | SSH 秘密鍵 |
| `-o allow_other` | （フラグ） | CSI DaemonSet (root) がマウントしたファイルにユーザーコンテナ (UID 1000) からアクセスするために必須 |
| `-o umask=000` | （フラグ） | hostUsers: false 環境で host UID 0 がコンテナ内で unmapped になるため、全 UID に rwxrwxrwx を付与 |
| `-f` | （フラグ） | フォアグラウンド起動（プロセス管理のため必須） |

### 4.3 s3fs マウントオプション

| オプション | 値 | 理由 |
|-----------|-----|------|
| `-o passwd_file` | 一時ファイルパス | S3 認証情報（`ACCESS_KEY:SECRET_KEY` 形式） |
| `-o url` | `${endpoint}` | S3 エンドポイント URL |
| `-o endpoint` | `${region}` | リージョン |
| `-o use_path_request_style` | （フラグ） | MinIO 等のパスベース S3 互換サービス対応 |
| `-o allow_other` | （フラグ） | sshfs と同様 |
| `-o umask=000` | （フラグ） | sshfs と同様 |
| `-o no_check_certificate` | 条件付き | 自己署名証明書環境（`noCheckCert: "true"` のとき） |
| `-f` | （フラグ） | フォアグラウンド起動 |

### 4.4 CSIDriver リソース

```yaml
apiVersion: storage.k8s.io/v1
kind: CSIDriver
metadata:
  name: fuse.csi.fuse-k8s.io
spec:
  attachRequired: false      # PV なしでエフェメラルボリュームとして利用
  podInfoOnMount: true       # Pod 情報を volumeContext に付加
  volumeLifecycleModes:
    - Ephemeral              # エフェメラルボリュームのみサポート
```

### 4.5 CSI DaemonSet リソース要件

マルチユーザー（最大 50 名）を想定したリソース設計:

| 項目 | 算定根拠 |
|------|---------|
| CPU request: `500m` | ドライバー本体のベース消費 |
| CPU limit: `4` | 50 プロセス × 50m = 2500m + 余裕 |
| Memory request: `512Mi` | ドライバー本体 + OS バッファ |
| Memory limit: `4Gi` | 50 プロセス × 64Mi = 3200Mi + 余裕 |

---

## 5. インターフェース設計

### 5.1 ユーザー Pod の CSI ボリューム定義

ユーザーが書く Pod マニフェストのボリューム定義:

**sshfs の場合**:

```yaml
volumes:
  - name: sshfs-vol
    csi:
      driver: fuse.csi.fuse-k8s.io
      volumeAttributes:
        type: sshfs                    # 必須: "sshfs" または "s3fs"
        host: "ssh-server.example.com" # 必須: SSH ホスト名
        user: "remote-user"            # 必須: SSH ユーザー名
        remotePath: "/data/shared"     # 必須: リモートパス
        port: "22"                     # 省略可: デフォルト 22
        strictHostKeyCheck: "true"     # 省略可: デフォルト true
      nodePublishSecretRef:
        name: ssh-key                  # Secret 名（同一 namespace に作成）
```

**s3fs の場合**:

```yaml
volumes:
  - name: s3fs-vol
    csi:
      driver: fuse.csi.fuse-k8s.io
      volumeAttributes:
        type: s3fs                               # 必須
        bucket: "my-bucket"                      # 必須: バケット名
        endpoint: "http://minio.example.com:9000" # 必須: エンドポイント
        region: "us-east-1"                      # 省略可: デフォルト us-east-1
        noCheckCert: "false"                     # 省略可: デフォルト false
      nodePublishSecretRef:
        name: s3-credentials
```

### 5.2 Secret 形式

**sshfs 用**:

```bash
# 注意: --from-file を使うこと。--from-literal はシェル展開で末尾改行を除去するため
#        OpenSSH が "error in libcrypto" で鍵を読み込めない。
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/id_ed25519 \
  -n <tenant-namespace>
```

**s3fs 用**:

```bash
kubectl create secret generic s3-credentials \
  --from-literal=access_key="ACCESS_KEY" \
  --from-literal=secret_key="SECRET_KEY" \
  -n <tenant-namespace>
```

### 5.3 volumeAttributes パラメータ一覧

| パラメータ | 対象 | 必須 | デフォルト | 説明 |
|-----------|------|------|----------|------|
| `type` | 共通 | ✅ | - | `sshfs` または `s3fs` |
| `host` | sshfs | ✅ | - | SSH ホスト名 / IP |
| `user` | sshfs | ✅ | - | SSH ユーザー名 |
| `remotePath` | sshfs | ✅ | - | リモートマウントパス |
| `port` | sshfs | - | `22` | SSH ポート |
| `strictHostKeyCheck` | sshfs | - | `true` | `false` でホスト鍵検証を無効化（テスト用） |
| `bucket` | s3fs | ✅ | - | S3 バケット名 |
| `endpoint` | s3fs | ✅ | - | S3 エンドポイント URL |
| `region` | s3fs | - | `us-east-1` | S3 リージョン |
| `noCheckCert` | s3fs | - | `false` | `true` で TLS 証明書検証を無効化 |

---

## 6. セキュリティ設計

### 6.1 権限分離

```
┌─────────────────────────────────────────────────────────┐
│  管理者が管理するコンポーネント              privileged  │
│  - fuse-csi-driver DaemonSet (fuse-csi-system)          │
│  - Capsule operator (capsule-system)                     │
│  - Kyverno (kyverno)                                     │
└─────────────────────────────────────────────────────────┘
                    ▲
                    │ CSI NodePublishVolume（gRPC）
                    │
┌─────────────────────────────────────────────────────────┐
│  ユーザーが管理するコンポーネント          unprivileged │
│  - User Pod (テナント namespace)                         │
│    - hostUsers: false（Kyverno が強制）                 │
│    - runAsNonRoot: true, runAsUser: 1000                  │
│    - allowPrivilegeEscalation: false                     │
│    - capabilities.drop: ALL                              │
│    - seccompProfile: RuntimeDefault                      │
└─────────────────────────────────────────────────────────┘
```

### 6.2 hostUsers: false によるユーザー名前空間分離

`hostUsers: false` を有効にすることで、コンテナ内の UID がホスト上の別の UID にマッピングされる。

```
コンテナ内 UID 1000  →  ホスト上 UID 66536 (= 65536 + 1000)
コンテナ内 UID 0     →  ホスト上 UID 65536
```

これにより、コンテナ内でルート権限に昇格しても、ホスト上では非特権ユーザーとなり、ホストへの影響が限定される。

### 6.3 FUSE マウントのアクセス制御

CSI DaemonSet（host UID 0）がマウントした FUSE ファイルシステムに、`hostUsers: false` な user-namespaced コンテナからアクセスするための設定:

```
CSI DaemonSet → sshfs/s3fs を -o allow_other -o umask=000 でマウント
                                         │
                                         ▼
                         ホスト上: uid=0 でマウント、全パーミッション 777
                                         │
                              kubelet bind-mount
                                         │
                                         ▼
                     コンテナ内: ファイルオーナーは unmapped (nobody=65534)
                                 パーミッション 777 → UID 1000 から読み書き可
```

### 6.4 認証情報の保護

- SSH 秘密鍵・S3 認証情報は Kubernetes Secret に保存
- CSI driver が `nodePublishSecretRef` 経由で受け取り、一時ファイルに書き出す
- 一時ファイルは NodeUnpublishVolume 時に必ず削除（`defer os.Remove(keyFile)`）
- 一時ファイルのパーミッションは 0600

### 6.5 テナント分離

- 各 Pod の CSI エフェメラルボリュームは、kubelet が管理する Pod 固有のパス（`/var/lib/kubelet/pods/<pod-uid>/...`）にマウントされる
- 異なる Pod のマウントポイントには他の Pod からアクセスできない（kubelet が分離を保証）
- Capsule の Tenant 機能により、テナント間の namespace 分離も保証される

---

## 7. マルチテナント設計

### 7.1 Capsule Tenant 構成

```yaml
apiVersion: capsule.clastix.io/v1beta2
kind: Tenant
metadata:
  name: <tenant-name>
spec:
  owners:
    - kind: User
      name: <username>
  namespaceOptions:
    additionalMetadata:
      labels:
        pod-security.kubernetes.io/enforce: restricted  # PSS 強制
        pod-security.kubernetes.io/audit: baseline
        pod-security.kubernetes.io/warn: baseline
```

### 7.2 Kyverno ポリシー

**hostUsers: false 強制** (`policy/kyverno-force-userns.yaml`):
- Capsule テナントに属する全 Pod に `spec.hostUsers: false` を自動注入
- ユーザーは意識しなくてよい

**securityContext 自動注入** (`policy/kyverno-force-securecontext.yaml`):
- `+(key): value` 構文（未設定時のみ適用）で以下を注入:
  - `spec.securityContext.runAsNonRoot: true`
  - `spec.securityContext.runAsUser: 1000`
  - `spec.securityContext.seccompProfile.type: RuntimeDefault`
  - `containers[*].securityContext.allowPrivilegeEscalation: false`
  - `containers[*].securityContext.capabilities.drop: [ALL]`

### 7.3 CSI DaemonSet の namespace 分離

`fuse-csi-system` namespace は Capsule テナントに含めない。理由:
- PSS `privileged` が必要（`restricted` と両立しない）
- クラスター管理者のみが操作する（テナントユーザーからアクセス不可）

---

## 8. デプロイ構成

### 8.1 デプロイ順序

```
1. helm install capsule         (管理者)
2. helm install kyverno         (管理者)
3. kubectl apply -f csi/fuse-csi-driver.yaml           (CSIDriver + namespace)
4. kubectl apply -f csi/fuse-csi-driver-daemonset.yaml (DaemonSet)
5. kubectl apply -f policy/capsule-tenant-example.yaml (Tenant)
6. kubectl apply -f policy/kyverno-force-userns.yaml
7. kubectl apply -f policy/kyverno-force-securecontext.yaml
8. ユーザーが Secret + Pod を作成
```

### 8.2 GitHub Actions による自動ビルド

main / feature ブランチへの push で以下のイメージを自動ビルド:

| イメージ | タグ |
|---------|------|
| `ghcr.io/scaleworx-inc/fuse_k8s-fuse-csi-driver` | `latest`, `YYYYMMDD-<sha>` |

---

## 9. 既知の制約・注意事項

| 項目 | 内容 |
|------|------|
| Kubernetes バージョン | `hostUsers: false` 安定版は K8s 1.30+。1.28〜1.29 は beta、1.25〜1.27 は alpha（feature gate 有効化が必要）。 |
| カーネルバージョン | Linux 4.18+ が必要。`/proc/sys/kernel/unprivileged_userns_clone=1` が必要（distro 依存）。 |
| CSI DaemonSet 再起動 | 再起動で管理中の sshfs/s3fs プロセスが失われ、既存 Pod のマウントが壊れる。ユーザー Pod を再起動することで回復可能。 |
| volumeAttributes の制限 | 文字列のみ。ConfigMap からの値の参照不可。接続情報は volumeAttributes に直書きまたは Secret 経由。 |
| Secret の namespace | Secret は Pod と同じ namespace に作成が必要（テナント namespace ごとに作成）。 |
| FUSE 書き込み権限 | `umask=000` により全 UID から書き込み可だが、実際にはリモートサーバー / S3 バケット側のアクセス権にも依存する。 |
| `+` prefix の動作 | Kyverno の `+(key): value` は未設定時のみ適用。ユーザーが明示的に securityContext を指定した場合はその値が優先される。 |
