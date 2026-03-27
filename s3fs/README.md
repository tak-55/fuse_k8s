# s3fs マウント利用ガイド

このディレクトリは、FUSE CSI ドライバーを使って s3fs を利用するためのマニフェストを提供します。

## 前提

- クラスタ管理者が CSI ドライバーをデプロイ済み
  - `csi/fuse-csi-driver.yaml`
  - `csi/fuse-csi-driver-daemonset-prod.yaml`
- Capsule/Kyverno ポリシーが適用済み（securityContext 自動注入）

## 含まれるファイル

- `deploy.yaml`: 本番向け Pod サンプル
- `deploy-kind.yaml`: kind 検証向け Pod サンプル

> 運用方針: `deploy.yaml` の `volumeAttributes` を直接編集して使用します。  

## 1. Secret の作成

```bash
kubectl create secret generic s3-credentials \
  --from-literal=access_key=YOUR_ACCESS_KEY \
  --from-literal=secret_key=YOUR_SECRET_KEY \
  -n <your-namespace>
```

## 2. マニフェストの調整

`deploy.yaml` の `volumeAttributes` を環境に合わせます。

- `bucket`: バケット名
- `endpoint`: S3 互換エンドポイント（例: MinIO, AWS S3）
- `region`: リージョン
- `noCheckCert`: 自己署名証明書の場合は `true`

## 3. デプロイ

```bash
kubectl apply -f s3fs/deploy.yaml -n <your-namespace>
```

## 4. 動作確認

```bash
kubectl get pod -n <your-namespace>
kubectl logs <pod-name> -c s3fs-sidecar -n <your-namespace>
kubectl exec <pod-name> -c app -n <your-namespace> -- mount | grep fuse
kubectl exec <pod-name> -c app -n <your-namespace> -- ls -la /data
```

## トラブルシュート

### 認証エラー

Secret のキー名と値を確認します（`access_key`, `secret_key`）。

```bash
kubectl get secret s3-credentials -n <your-namespace> -o yaml
```

### マウント失敗

```bash
kubectl describe pod <pod-name> -n <your-namespace>
kubectl logs <pod-name> -c s3fs-sidecar -n <your-namespace>
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver
```

## 関連

- ルート概要: [`../README.md`](../README.md)
