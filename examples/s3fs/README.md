# s3fs マウント設定ガイド

Kubernetes Pod 内で FUSE を使用して S3 互換ストレージをマウントします。

## 概要

このマニフェストは、CSI ドライバーから fd-passing で FUSE ファイルディスクリプタを受け取り、非特権 Pod で s3fs を実行します。

## 前提条件

- Kubernetes 1.29 以上（sidecar initContainer 機能が必要）
- FUSE CSI ドライバーが `fuse-csi-system` namespace に導入済み
- Kyverno でセキュリティコンテキストが自動注入される環境
- S3 互換ストレージへのネットワーク接続可能
  - AWS S3
  - MinIO
  - Ceph
  - その他 S3 互換サービス

## セットアップ手順

### 1) Secret 作成

S3 アクセスキーと シークレットキーを Secret として作成します。

```bash
kubectl create secret generic s3-credentials \
  --from-literal=access_key=YOUR_ACCESS_KEY \
  --from-literal=secret_key=YOUR_SECRET_KEY \
  -n <tenant-namespace>
```

### 2) マニフェストをコピー

```bash
cp deploy.yaml my-s3fs-pod.yaml
```

### 3) volumeAttributes をカスタマイズ

以下のパラメータを編集します：

| パラメータ | 説明 | 例 |
|-----------|------|-----|
| `bucket` | S3 バケット名 | `my-bucket`, `data-storage` |
| `endpoint` | S3 エンドポイント | `https://s3.amazonaws.com` (AWS), `http://minio.svc:9000` (MinIO), `https://s3.example.com` |
| `region` | AWS リージョン（AWS S3 の場合） | `us-east-1`, `ap-northeast-1` |
| `noCheckCert` | SSL 証明書検証スキップ（自己署名証明書の場合） | `false` (本番), `true` (テスト/自己署名) |

```yaml
volumes:
  - name: s3fs-vol
    csi:
      driver: fuse.csi.fuse-k8s.io
      volumeAttributes:
        type: s3fs
        bucket: "your-bucket"
        endpoint: "https://your-s3-endpoint"
        region: "us-east-1"
        noCheckCert: "false"
```

### 4) Pod をデプロイ

```bash
kubectl apply -f my-s3fs-pod.yaml -n <tenant-namespace>
```

### 5) 動作確認

```bash
# Pod の状態確認
kubectl get pod -n <tenant-namespace>

# マウント確認
kubectl exec <pod-name> -c app -n <tenant-namespace> -- mount | grep fuse

# ログ確認
kubectl logs <pod-name> -c s3fs-sidecar -n <tenant-namespace>
```

## S3 エンドポイント別の設定例

### AWS S3

```yaml
volumeAttributes:
  type: s3fs
  bucket: "my-bucket"
  endpoint: "https://s3.amazonaws.com"
  region: "us-east-1"
  noCheckCert: "false"
```

### MinIO（ローカル）

```yaml
volumeAttributes:
  type: s3fs
  bucket: "my-bucket"
  endpoint: "http://minio.default.svc.cluster.local:9000"
  region: "us-east-1"
  noCheckCert: "true"  # 自己署名証明書を使う場合
```

### Ceph（RADOS Gateway）

```yaml
volumeAttributes:
  type: s3fs
  bucket: "my-bucket"
  endpoint: "https://rgw.example.com"
  region: "default"
  noCheckCert: "false"
```

## よくある問題

### Pod が ContainerCreating で止まっている

**確認:**
```bash
kubectl describe pod <pod-name> -n <tenant-namespace>
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver
```

通常は以下の原因です：
- CSI ドライバーがノード上で起動していない
- endpoint が到達不可
- Secret が作成されていない

### アクセスキーエラー

**確認:**
```bash
kubectl logs <pod-name> -c s3fs-sidecar -n <tenant-namespace>
```

"Access Denied" エラーが出た場合：
- Secret の access_key / secret_key が正しいか確認
- S3 側のバケットアクセス権限を確認
- IAM ポリシー（AWS の場合）でバケットアクセスが許可されているか確認

### SSL 証明書エラー

**確認:**
```bash
kubectl logs <pod-name> -c s3fs-sidecar -n <tenant-namespace> | grep -i cert
```

自己署名証明書を使用している場合は、`noCheckCert: "true"` に設定してください。

```yaml
volumeAttributes:
  noCheckCert: "true"
```

本番環境ではオレオレ証明書ではなく、信頼された CA から取得した証明書の使用を推奨します。

## パフォーマンス考慮事項

- s3fs は HTTP ベースなため、ローカルファイルシステムより レイテンシが大きい
- 小さなファイルの頻繁な読み書きより、大きなファイルの一括操作に向いている
- ネットワーク遅延を考慮したアプリケーション実装が必要

## 参考

- メインの README: [`../../../README.md`](../../../README.md)
- CSI ドライバー実装: [`../../../fuse-csi-driver`](../../../fuse-csi-driver)
- s3fs-sidecar 実装: [`../../../s3fs-sidecar`](../../../s3fs-sidecar)
