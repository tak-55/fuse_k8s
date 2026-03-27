# sshfs マウント利用ガイド

このディレクトリは、FUSE CSI ドライバーを使って sshfs を利用するためのマニフェストを提供します。

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

SSH 秘密鍵は **`--from-file`** で作成してください。  
`--from-literal` は改行欠落で `error in libcrypto` の原因になります。

```bash
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/id_ed25519 \
  -n <your-namespace>
```

## 2. マニフェストの調整

`deploy.yaml` の `volumeAttributes` を環境に合わせます。

- `host`: SSH サーバーのホスト名/IP
- `user`: SSH ユーザー
- `remotePath`: リモートマウントパス
- `port`: SSH ポート
- `strictHostKeyCheck`: 本番は `accept-new` 推奨

## 3. デプロイ

```bash
kubectl apply -f sshfs/deploy.yaml -n <your-namespace>
```

## 4. 動作確認

```bash
kubectl get pod -n <your-namespace>
kubectl logs <pod-name> -c sshfs-sidecar -n <your-namespace>
kubectl exec <pod-name> -c app -n <your-namespace> -- mount | grep fuse
kubectl exec <pod-name> -c app -n <your-namespace> -- ls -la /data
```

## トラブルシュート

### `error in libcrypto`

Secret を再作成します。

```bash
kubectl delete secret ssh-key -n <your-namespace>
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/id_ed25519 \
  -n <your-namespace>
```

### マウント失敗

```bash
kubectl describe pod <pod-name> -n <your-namespace>
kubectl logs <pod-name> -c sshfs-sidecar -n <your-namespace>
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver
```

## 関連

- ルート概要: [`../README.md`](../README.md)
