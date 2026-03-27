# 実装変更報告書

> **基準文書**: [FUSE向け汎用CSIドライバ meta-fuse-csi-plugin 調査報告書](./meta-fuse-csi-plugin-report.md)
> **対象リポジトリ**: fuse_k8s（本リポジトリ）

本文書は、調査レポートの内容に対して本リポジトリの実装で変更・決定した事項をまとめたものです。

---

## 1. Kubernetes バージョン要件の引き上げ

| | 調査レポート | 本実装 |
|---|---|---|
| **要件** | K8s 1.20 以上推奨 | **K8s v1.29+ 必須** |

**変更理由**: Kubernetes v1.29 で GA となった [SidecarContainers](https://kubernetes.io/docs/concepts/workloads/pods/sidecar-containers/) 機能（`initContainers` + `restartPolicy: Always`）を前提とした設計を採用したため。

---

## 2. サイドカーコンテナのデプロイ方式

| | 調査レポート | 本実装 |
|---|---|---|
| **定義場所** | `containers`（通常コンテナ） | `initContainers` + `restartPolicy: Always` |
| **起動順序制御** | `while` ループでマウント待機、または `startupProbe` | `startupProbe` で保証（成功するまで app コンテナは起動しない） |

**変更理由**: Native sidecar により、FUSE マウント完了を保証してからアプリケーションコンテナを起動する信頼性の高い順序制御が可能になりました。調査レポートで紹介されていた `while` ループによるポーリング方式は不要となりました。

---

## 3. マウントアプローチの選定

| | 調査レポート | 本実装 |
|---|---|---|
| **アプローチ** | fuse-starter / fusermount3-proxy の2種 | **fusermount3-proxy のみ** |

**変更理由**: 本実装の対象である sshfs・s3fs はいずれも libfuse3 ベースであり、fusermount3-proxy アプローチで統一的に対応可能です。fuse-starter は不採用としました。

**実装方式の詳細**:

調査レポートでは「FUSE 実装が fusermount3 の呼び出しに失敗すると fusermount3-proxy が代わりに起動」と記載されていますが、本実装ではより直接的な方式を採用しました。

```
Dockerfile:
  COPY --from=builder /bin/fusermount3-proxy /bin/fusermount3

entrypoint.sh:
  touch /dev/fuse
```

1. fusermount3-proxy バイナリを `/bin/fusermount3` として直接配置します（バイナリ差し替え）
2. `touch /dev/fuse` で通常ファイルを作成し、libfuse が `/dev/fuse` をキャラクターデバイスとして open できないようにして fusermount3 経由パスにフォールバックさせます

---

## 4. CSI ドライバーマニフェストの独自管理

| | 調査レポート | 本実装 |
|---|---|---|
| **マニフェスト** | 上流の `./deploy/` をそのまま使用 | `csi/` ディレクトリに独自マニフェストを配置 |
| **RBAC** | 記載なし | ServiceAccount, ClusterRole, ClusterRoleBinding を追加 |
| **イメージバージョン** | `latest` | `v0.2.2` 固定 |

**変更内容**:

- `csi/csi-driver.yaml`: Namespace (`mfcp-system`) + CSIDriver リソース定義
- `csi/csi-driver-daemonset.yaml`: DaemonSet + RBAC（pods の get/list/watch、events の create/patch 権限）
- CSI ドライバーコンテナに加え `node-driver-registrar` (v2.10.0) を含む2コンテナ構成
- master/control-plane ノードへの tolerations を設定しています

---

## 5. 対象 FUSE 実装の限定

| | 調査レポート | 本実装 |
|---|---|---|
| **対象** | mountpoint-s3, goofys, s3fs, ros3fs, gcsfuse, sshfs（6種） | **sshfs, s3fs（2種）** |

**変更理由**: 実運用で必要な sshfs（SSH リモートマウント）と s3fs（S3 互換ストレージ）に絞り、それぞれ独立したディレクトリに以下のファイルを整備しました。

```
sshfs/
  ├── Dockerfile
  ├── entrypoint.sh
  ├── deploy-kind.yaml
  ├── deploy.yaml
  └── README.md
s3fs/
  ├── Dockerfile
  ├── entrypoint.sh
  ├── deploy-kind.yaml
  ├── deploy.yaml
  └── README.md
```

---

## 6. Docker イメージのビルド・配布

| | 調査レポート | 本実装 |
|---|---|---|
| **イメージ** | 上流の example イメージを使用 | 独自 Dockerfile でビルド |
| **ビルド方式** | 手動 | GitHub Actions で自動ビルド・プッシュ |
| **タグ付け** | なし | `latest` + `YYYYMMDD-<commit sha>` |
| **レジストリ** | ghcr.io/pfnet-research/... | ghcr.io/<本リポジトリ>-{sshfs,s3fs} |

**実装内容**:

- マルチステージビルド: Stage 1 で meta-fuse-csi-plugin ソースから fusermount3-proxy を Go ビルド、Stage 2 で sshfs/s3fs サイドカーイメージを構成
- GitHub Actions (`.github/workflows/docker-image.yml`) で main ブランチへの push 時に matrix strategy で sshfs/s3fs を並列ビルド

---

## 7. 認証情報の管理方式

| | 調査レポート | 本実装 |
|---|---|---|
| **方式** | 環境変数にハードコード（テスト用） | **Kubernetes Secret** を使用 |

**実装内容**:

| FUSE 実装 | Secret 名 | キー | 展開方法 |
|---|---|---|---|
| sshfs | `ssh-key` | `private_key` | `secretKeyRef` → 環境変数 → entrypoint.sh でファイル出力 |
| s3fs | `s3-credentials` | `access_key`, `secret_key` | `secretKeyRef` → 環境変数 → entrypoint.sh で passwd-s3fs 生成 |

---

## 8. CSI ボリューム属性の明示化

| | 調査レポート | 本実装 |
|---|---|---|
| **volumeAttributes** | `fdPassingEmptyDirName` のみ | 4属性を明示的に設定 |

**本実装の volumeAttributes**:

```yaml
volumeAttributes:
  socketDir: /var/lib/mfcp/uds
  socketName: mfcp.sock
  fdPassingEmptyDirName: fuse-socket-dir
  fdPassingSocketName: mfcp.sock
```

Unix Domain Socket (UDS) のソケットパスを `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` 環境変数と一致させる必要があります。

---

## 9. セキュリティコンテキストの明示設定

| | 調査レポート | 本実装 |
|---|---|---|
| **securityContext** | `privileged: true` のみ（一部コンテナ） | 全コンテナに `runAsNonRoot: false` を明示設定 |

**変更理由**: Pod Security Admission が有効な環境で、コンテナイメージのデフォルト設定に依存せずコンテナの起動を保証するため。

**設定対象**:

| 対象 | 設定 |
|------|------|
| CSI DaemonSet (csi-driver) | `privileged: true` + `runAsNonRoot: false` |
| CSI DaemonSet (node-driver-registrar) | `runAsNonRoot: false` |
| FUSE sidecar | `privileged: true` + `runAsNonRoot: false` |
| app コンテナ | `runAsNonRoot: false` |

---

## 10. リソース制限の追加

調査レポートの Pod マニフェスト例には `resources` の記載がありませんでしたが、本実装では全コンテナに設定を追加しました。

| コンテナ | CPU requests | CPU limits | Memory requests | Memory limits |
|---|---|---|---|---|
| FUSE sidecar | 50m | 200m | 64Mi | 256Mi |
| app | 10m | 100m | 32Mi | 128Mi |
| csi-driver | 50m | 200m | 64Mi | 256Mi |
| node-driver-registrar | 10m | 50m | 20Mi | 100Mi |

---

## 11. ConfigMap による環境固有値の外出し

| | 調査レポート | 本実装 |
|---|---|---|
| **接続パラメータ** | Pod spec 内の `env` に直接記載 | **ConfigMap** (`sshfs-config` / `s3fs-config`) から `configMapKeyRef` で注入 |

**変更理由**: 接続パラメータ（ホスト、ユーザー、バケット名等）を Pod マニフェストから分離し、環境ごとに異なる設定値を安全に管理するため。

**実装内容**:

- `configmap.example.yaml` をテンプレートとして各 FUSE ディレクトリに配置
- 利用者はテンプレートをコピーして環境別の ConfigMap を作成（例: `configmap.yaml`）
- `configmap.example.yaml` 以外の `configmap*.yaml` は `.gitignore` で除外し、環境固有値がリポジトリにコミットされることを防止
- `deploy-kind.yaml` / `deploy.yaml` の両方が同一の ConfigMap 名を参照するため、マニフェスト自体の編集は不要

| FUSE 実装 | ConfigMap 名 | キー |
|---|---|---|
| sshfs | `sshfs-config` | `SSHFS_HOST`, `SSHFS_USER`, `SSHFS_REMOTE_PATH`, `SSHFS_PORT` |
| s3fs | `s3fs-config` | `S3FS_BUCKET`, `S3FS_ENDPOINT`, `S3FS_REGION` |

---

## 変更一覧

| # | 変更項目 | 関連ファイル |
|---|---|---|
| 1 | K8s バージョン要件の引き上げ（v1.29+） | `sshfs/deploy-kind.yaml`, `sshfs/deploy.yaml`, `s3fs/deploy-kind.yaml`, `s3fs/deploy.yaml` |
| 2 | Native sidecar 方式の採用 | `sshfs/deploy-kind.yaml`, `sshfs/deploy.yaml`, `s3fs/deploy-kind.yaml`, `s3fs/deploy.yaml` |
| 3 | fusermount3-proxy のみ採用 | `sshfs-sidecar/*`, `s3fs-sidecar/*` |
| 4 | CSI マニフェストの独自管理 | `csi/csi-driver.yaml`, `csi/csi-driver-daemonset.yaml` |
| 5 | 対象を sshfs・s3fs に限定 | `sshfs/`, `s3fs/` |
| 6 | 独自イメージビルド + CI/CD | `*/Dockerfile`, `.github/workflows/docker-image.yml` |
| 7 | Secret による認証情報管理 | `*/deploy-kind.yaml`, `*/deploy.yaml` |
| 8 | CSI ボリューム属性の明示化 | `*/deploy-kind.yaml`, `*/deploy.yaml` |
| 9 | セキュリティコンテキストの明示設定 | `*/deploy-kind.yaml`, `*/deploy.yaml`, `csi/csi-driver-daemonset.yaml` |
| 10 | 全コンテナへのリソース制限追加 | `*/deploy-kind.yaml`, `*/deploy.yaml`, `csi/csi-driver-daemonset.yaml` |
| 11 | ConfigMap による環境固有値の外出し | `*/configmap.example.yaml`, `*/deploy-kind.yaml`, `*/deploy.yaml` |
