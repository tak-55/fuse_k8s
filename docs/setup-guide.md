# fuse_k8s セットアップガイド

本ガイドは、`fuse-csi-driver` を新規導入し、sshfs/s3fs を利用可能にするための最短手順です。

## 前提条件

| 項目 | 要件 |
|---|---|
| Kubernetes | v1.29+ |
| ノード OS | Linux |
| FUSE | ノードで `/dev/fuse` が利用可能 |
| 権限 | `fuse-csi-system` で privileged Pod 実行可 |

## 導入手順

### リポジトリ取得

```bash
git clone https://github.com/ScaleWorX-Inc/fuse_k8s.git
cd fuse_k8s
```

### CSI ドライバーのデプロイ

クラスター種別に応じて DaemonSet マニフェストを選択します。

```bash
kubectl apply -f csi/fuse-csi-driver.yaml

# kind / devcontainer
kubectl apply -f csi/fuse-csi-driver-daemonset.yaml

# kubeadm / RKE2
# kubectl apply -f csi/fuse-csi-driver-daemonset-prod.yaml

# k3s
# kubectl apply -f csi/fuse-csi-driver-daemonset-k3s.yaml
```

### デプロイ確認

```bash
kubectl get ds -n fuse-csi-system
kubectl get pods -n fuse-csi-system
kubectl get csidriver fuse.csi.fuse-k8s.io
```

## 認証情報の準備

### sshfs

```bash
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/id_ed25519 \
  -n <tenant-namespace>
```

### s3fs

```bash
kubectl create secret generic s3-credentials \
  --from-literal=access_key=YOUR_ACCESS_KEY \
  --from-literal=secret_key=YOUR_SECRET_KEY \
  -n <tenant-namespace>
```

## 利用手順

接続先パラメータは ConfigMap ではなく、`volumeAttributes` に直接記述します。

- sshfs: `sshfs/README.md`
- s3fs: `s3fs/README.md`

## プライベートレジストリ利用時

```bash
kubectl create secret docker-registry ghcr-secret \
  --docker-server=ghcr.io \
  --docker-username=<GITHUB_USERNAME> \
  --docker-password=<GITHUB_PAT> \
  -n fuse-csi-system
```

その後、CSI DaemonSet と tenant 側 Pod の両方で `imagePullSecrets` を設定してください。

## トラブルシューティング

### CSI Pod が起動しない

```bash
kubectl describe pod -n fuse-csi-system <fuse-csi-driver-pod>
```

### ユーザー Pod がマウント失敗する

```bash
kubectl describe pod -n <tenant-namespace> <pod>
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver -c fuse-csi-driver --tail=200
```

### `/dev/fuse` がない

対象ノードで `modprobe fuse` を実行し、`ls -l /dev/fuse` を確認してください。
