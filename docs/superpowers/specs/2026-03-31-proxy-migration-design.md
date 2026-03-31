# fusermount3-proxy 移行設計

**日付:** 2026-03-31
**参照:** https://github.com/tak-55/meta-fuse-csi-plugin
**ステータス:** 承認済み

---

## 概要

現在の `fusermount3-stub` + receiver による fd-passing 方式（push モデル）を、
meta-fuse-csi-plugin の `fusermount3-proxy` 方式（pull モデル）に置き換える。

sshfs・s3fs 両方が対象。kind 関連ファイルはすべて削除する。

---

## アーキテクチャ変更

### 変更前（stub/push）

```
CSI: /dev/fuse open → mount(2) → UDS listen
  → receiver が UDS 接続 → fd + credentials JSON を受信
  → receiver: FUSE_PREOPEN_FD={fd} 環境変数セット → sshfs/s3fs exec
  → libfuse が fusermount3-stub 呼び出し
  → stub: FUSE_PREOPEN_FD の fd を _FUSE_COMMFD 経由で libfuse に返す
```

### 変更後（proxy/pull）

```
CSI: /dev/fuse open → mount(2) → params.json + creds.json を emptyDir 書き出し → UDS listen
  → receiver: params.json + creds.json 読み込み
           → FUSERMOUNT3PROXY_FDPASSING_SOCKPATH=/fuse-fd/csi.sock セット
           → sshfs/s3fs exec
  → libfuse が fusermount3-proxy 呼び出し
  → proxy: CSI UDS に接続 → fd を SCM_RIGHTS で受信 → _FUSE_COMMFD 経由で libfuse に返す
```

### 主な差分

| | 変更前 (stub) | 変更後 (proxy) |
|--|--|--|
| fd の経路 | CSI → receiver（起動時）→ stub → libfuse | proxy → CSI（libfuse 呼び出し時）→ libfuse |
| credentials の経路 | UDS 経由で CSI → receiver | emptyDir の creds.json → receiver |
| fusermount3 の役割 | FUSE_PREOPEN_FD 環境変数から fd を取得 | CSI UDS に自分で接続して fd を取得 |
| receiver の役割 | UDS 接続 + fd/creds 受信 + FUSE_PREOPEN_FD セット | ファイル読み込み + FUSERMOUNT3PROXY_FDPASSING_SOCKPATH セット |
| CSI fdpassing.go | fd + credentials JSON を UDS 送信 | fd のみ UDS 送信、creds は emptyDir に書き出し |

---

## 変更ファイル一覧

### 変更

| ファイル | 変更内容 |
|--|--|
| `fuse-csi-driver/pkg/driver/fdpassing.go` | `FuseCreds` を UDS 送信から削除。`creds.json` を emptyDir に書き出し（パーミッション 0600）。UDS は fd のみ送信 |
| `sshfs-sidecar/fusermount3-stub/main.go` → `fusermount3-proxy/main.go` | 全面書き換え：CSI UDS 接続 → fd 受信 → _FUSE_COMMFD 経由で libfuse に返す |
| `s3fs-sidecar/fusermount3-stub/main.go` → `fusermount3-proxy/main.go` | 同上 |
| `sshfs-sidecar/receiver/main.go` | UDS 接続・fd 受信ロジック削除。`creds.json` 読み込みに変更。`FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` セット後 sshfs exec |
| `s3fs-sidecar/receiver/main.go` | 同上（s3fs 用） |
| `sshfs-sidecar/Dockerfile` | ビルド対象を `fusermount3-stub` → `fusermount3-proxy` に変更 |
| `s3fs-sidecar/Dockerfile` | 同上 |

### 追加

| ファイル | 内容 |
|--|--|
| `examples/sshfs/deploy.yaml` | `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH=/fuse-fd/csi.sock` を含むサンプル manifest |
| `examples/s3fs/deploy.yaml` | 同上（s3fs 用） |

### 削除

| 対象 | 理由 |
|--|--|
| `sshfs/`（ディレクトリごと） | examples/ に移動 |
| `s3fs/`（ディレクトリごと） | examples/ に移動 |
| `csi/fuse-csi-driver-daemonset.yaml` | kind/devcontainer 専用 DaemonSet |
| `.devcontainer/`（全体） | kind 開発環境設定 |
| `docs/guide-kind-setup-and-test.md` | kind 手順書 |
| `tests/reports/kind-capsule-kyverno-report.txt` | kind テストレポート |
| `overlays/local/kind/`（存在する場合） | kind overlay |

---

## 詳細設計

### 1. fuse-csi-driver/pkg/driver/fdpassing.go

**変更点:**
- `FuseCreds` 構造体はそのまま残す（node.go から参照されているため）
- `startFdServer` の引数から `creds FuseCreds` を削除
- credentials は `creds.json` として emptyDir に書き出す（パーミッション 0600）
- UDS サーバーは fd のみ SCM_RIGHTS で送信（credentials JSON は送らない）
- `sendFdToSidecar` 関数を単純化（credentials JSON 送信を削除）

**creds.json のパーミッション:**
`0600`（CSI DaemonSet が root で書き出し、サイドカー UID 1000 が読み取り可能なよう chown 1000:1000）

**creds.json の構造（sshfs 用）:**
```json
{ "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n..." }
```

**creds.json の構造（s3fs 用）:**
```json
{ "access_key": "KEY", "secret_key": "SECRET" }
```

### 2. fusermount3-proxy/main.go（sshfs-sidecar・s3fs-sidecar 共通）

meta-fuse-csi-plugin の `cmd/fusermount3-proxy/main.go` をベースに移植。

**動作:**
1. `_FUSE_COMMFD` 環境変数から libfuse の通信用 fd を取得
2. `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` 環境変数から CSI UDS ソケットパスを取得
3. CSI UDS ソケットに接続し、fd を SCM_RIGHTS で受信
4. 受信した fd を `_FUSE_COMMFD` 経由で libfuse に送信して終了

**参照コード:** `pkg/fuse_starter/fuse_starter.go` の `PrepareMountConfig()` を利用

### 3. receiver/main.go（sshfs・s3fs）

**変更後の動作:**
1. `params.json` が現れるまで待機（最大 60 秒）
2. `creds.json` が現れるまで待機（最大 60 秒）
3. params・creds を読み込み
4. 認証情報を一時ファイルに書き出し（SSH 鍵 or s3fs passwd）
5. `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH=/fuse-fd/csi.sock` をセット
6. sshfs / s3fs を exec（fusermount3-proxy が libfuse から呼ばれ fd を取得）
7. `/fuse-fd/ready` を書き込み（readiness probe 用）

**削除するロジック:**
- UDS への接続
- SCM_RIGHTS による fd 受信
- `FUSE_PREOPEN_FD` 環境変数のセット
- `FD_CLOEXEC` のクリア

### 4. Dockerfile（sshfs-sidecar・s3fs-sidecar）

**変更点:**
- Go builder ステージのビルド対象を `fusermount3-stub` → `fusermount3-proxy` に変更
- コピー先も `/usr/bin/fusermount3` に変更（setuid 4755 はそのまま）

### 5. examples/sshfs/deploy.yaml・examples/s3fs/deploy.yaml

現在の `sshfs/deploy.yaml`・`s3fs/deploy.yaml` をベースに以下を追加:

```yaml
env:
- name: FUSERMOUNT3PROXY_FDPASSING_SOCKPATH
  value: "/fuse-fd/csi.sock"
```

---

## 環境変数まとめ

| 変数名 | 設定者 | 参照者 | 値 |
|--|--|--|--|
| `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` | receiver (exec 前にセット) | fusermount3-proxy | `/fuse-fd/csi.sock` |
| `_FUSE_COMMFD` | libfuse (fusermount3 呼び出し時) | fusermount3-proxy | libfuse の通信用 fd 番号 |

**削除される環境変数:**
- `FUSE_PREOPEN_FD`（stub 専用、不要になる）
- `FUSE_COMMFD`（stub の fallback 対応、不要になる）

---

## 非機能要件

- PSS restricted 準拠は変更なし（sidecar は UID 1000、非特権）
- `fusermount3-proxy` の setuid (4755) は引き続き必要（libfuse から setuid で呼ばれる）
- `allowPrivilegeEscalation: true` は sidecar コンテナに引き続き必要（setuid 実行のため）
