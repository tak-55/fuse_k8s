# S3互換ストレージ FUSE マウント (s3fs-fuse)

meta-fuse-csi-pluginを使用して、S3互換ストレージ（MinIO、Ceph、AWS S3等）をKubernetes Pod内でFUSEマウントするサイドカーコンテナの実装です。

## アーキテクチャ

```
[s3fs sidecar]
  └─ touch /dev/fuse            (libfuse に fusermount3 経由を強制)
  └─ s3fs ... -f                (フォアグラウンド起動)
  └─ fusermount3-proxy          (fusermount3 として差し替え済み)
       └─ UDS ─────────────────> [CSI DaemonSet (CAP_SYS_ADMIN)]
                                     └─ open("/dev/fuse") + mount()
[app container]
  └─ /data (HostToContainer 伝播)
```

## 前提条件

1. Kubernetes v1.29+ (SidecarContainers 機能が必要)
2. meta-fuse-csi-plugin の CSI DaemonSet がデプロイ済み
   ```bash
   kubectl apply -f ../deploy/csi-driver.yaml
   kubectl apply -f ../deploy/csi-driver-daemonset.yaml
   ```

## セットアップ手順

### 1. Docker イメージのビルド

```bash
cd s3fs
docker build -t s3fs-proxy:latest .
```

### 2. S3認証情報Secretの作成

```bash
kubectl create secret generic s3-credentials \
  --from-literal=access_key=YOUR_ACCESS_KEY \
  --from-literal=secret_key=YOUR_SECRET_KEY
```

### 3. デプロイマニフェストの編集

`deploy.yaml` の以下の環境変数を編集してください：

| 環境変数 | 説明 | 例 |
|---------|------|-----|
| `S3FS_BUCKET` | S3バケット名 | `my-bucket` |
| `S3FS_ENDPOINT` | S3エンドポイントURL | `http://minio.default.svc.cluster.local:9000` |
| `S3FS_REGION` | リージョン | `us-east-1` |

### 4. デプロイ

```bash
kubectl apply -f deploy.yaml
```

### 5. 動作確認

```bash
# Pod の起動確認
kubectl get pod s3fs-example

# マウント確認
kubectl exec -it s3fs-example -c app -- mount | grep fuse.s3fs

# ファイル一覧確認
kubectl exec -it s3fs-example -c app -- ls -la /data
```

## 環境変数

s3fs-proxy サイドカーは以下の環境変数で動作を制御できます：

| 変数名 | 必須 | デフォルト | 説明 |
|--------|------|-----------|------|
| `S3FS_BUCKET` | ✓ | - | S3バケット名 |
| `S3FS_ENDPOINT` | ✓ | - | S3エンドポイントURL |
| `S3FS_REGION` | | `us-east-1` | S3リージョン |
| `S3FS_MOUNT_POINT` | | `/mnt/s3fs` | マウント先パス |
| `AWS_ACCESS_KEY_ID` | ✓ | - | アクセスキー (Secretから注入) |
| `AWS_SECRET_ACCESS_KEY` | ✓ | - | シークレットキー (Secretから注入) |
| `S3FS_OPTS` | | (空) | 追加のs3fsオプション |
| `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` | ✓ | `/var/lib/mfcp/uds/mfcp.sock` | UDSソケットパス |

## s3fs オプションのカスタマイズ

追加のs3fsオプションは `S3FS_OPTS` 環境変数で指定できます：

```yaml
- name: S3FS_OPTS
  value: "-o dbglevel=info -o curldbg"
```

デフォルトで設定されているオプション：
- `-o passwd_file=/etc/passwd-s3fs` - 認証情報ファイルパス
- `-o url=${S3FS_ENDPOINT}` - S3エンドポイント
- `-o endpoint=${S3FS_REGION}` - リージョン
- `-o use_path_request_style` - パススタイルリクエスト使用
- `-o no_check_certificate` - SSL証明書検証スキップ
- `-f` - フォアグラウンド実行

## トラブルシューティング

### Pod が起動しない

```bash
# s3fs-proxy サイドカーのログを確認
kubectl logs s3fs-example -c s3fs-proxy

# CSI DaemonSet のログを確認
kubectl logs -n kube-system -l app=meta-fuse-csi-plugin
```

### マウントが成功しない

1. S3エンドポイントへの接続確認：
   ```bash
   kubectl exec -it s3fs-example -c s3fs-proxy -- curl -v ${S3FS_ENDPOINT}
   ```

2. 認証情報の確認：
   ```bash
   kubectl get secret s3-credentials -o yaml
   ```

3. s3fsのデバッグ出力を有効化：
   ```yaml
   - name: S3FS_OPTS
     value: "-o dbglevel=info -o curldbg"
   ```

### バケットが見えない

- バケット名が正しいか確認
- アクセスキーに適切な権限があるか確認
- リージョンの設定が正しいか確認

## 注意事項

- s3fs は POSIX 互換ですが、通常のファイルシステムと完全に同一ではありません
- パフォーマンスは S3 API のレイテンシに依存します
- 大量の小さいファイルの操作は遅くなる可能性があります
- 本番環境では SSL 証明書検証を有効化することを推奨します（`no_check_certificate` オプションを削除）

## 参考資料

- [meta-fuse-csi-plugin](https://github.com/pfnet-research/meta-fuse-csi-plugin)
- [s3fs-fuse](https://github.com/s3fs-fuse/s3fs-fuse)
