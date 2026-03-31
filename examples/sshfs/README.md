# sshfs マウント設定ガイド

Kubernetes Pod 内で FUSE を使用して SSH サーバー上のディレクトリをマウントします。

## 概要

このマニフェストは、CSI ドライバーから fd-passing で FUSE ファイルディスクリプタを受け取り、非特権 Pod で sshfs を実行します。

## 前提条件

- Kubernetes 1.29 以上（sidecar initContainer 機能が必要）
- FUSE CSI ドライバーが `fuse-csi-system` namespace に導入済み
- Kyverno でセキュリティコンテキストが自動注入される環境
- リモート SSH サーバーへのネットワーク接続可能

## セットアップ手順

### 1) Secret 作成

SSH 秘密鍵を Secret として作成します。**必ず `--from-file` を使用してください。**

```bash
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/sshfs_key \
  -n <tenant-namespace>
```

**重要:** `--from-literal` では使えません。シェル展開で末尾改行が除去されるため "error in libcrypto" が発生します。

### 2) マニフェストをコピー

```bash
cp deploy.yaml my-sshfs-pod.yaml
```

### 3) volumeAttributes をカスタマイズ

以下のパラメータを編集します：

| パラメータ | 説明 | 例 |
|-----------|------|-----|
| `host` | SSH サーバーのホスト名または IP | `ssh.example.com` / `10.0.1.50` / `ssh-server.default.svc.cluster.local` |
| `user` | SSH ログインユーザー | `root`, `appuser` |
| `remotePath` | マウントするリモートパス | `/data`, `/exports` |
| `port` | SSH ポート番号（デフォルト: 22） | `22`, `2222` |
| `strictHostKeyCheck` | ホストキー検証 | `accept-new` (本番), `false` (テスト) |

```yaml
volumes:
  - name: sshfs-vol
    csi:
      driver: fuse.csi.fuse-k8s.io
      volumeAttributes:
        type: sshfs
        host: "your-ssh-host"
        user: "your-user"
        remotePath: "/data"
        port: "22"
        strictHostKeyCheck: "accept-new"
```

### 4) セキュリティコンテキストの確認

マニフェストには PSS (Pod Security Standards) restricted に対応するため、すべてのコンテナに以下が設定されています：

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 1000
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
  seccompProfile:
    type: RuntimeDefault
```

**役割：**
- `allowPrivilegeEscalation: false` — 権限昇格を防止
- `capabilities.drop: ["ALL"]` — すべての Linux capabilities を削除（最小権限）
- `seccompProfile: RuntimeDefault` — デフォルト seccomp フィルタを適用
- `runAsNonRoot: true` / `runAsUser: 1000` — 非root ユーザーで実行

これにより、PSS restricted namespace（`enforce: restricted`）でもポッドをデプロイできます。

### 5) Pod をデプロイ

```bash
kubectl apply -f my-sshfs-pod.yaml -n <tenant-namespace>
```

### 5) 動作確認

```bash
# Pod の状態確認
kubectl get pod -n <tenant-namespace>

# マウント確認
kubectl exec <pod-name> -c app -n <tenant-namespace> -- mount | grep fuse

# ログ確認
kubectl logs <pod-name> -c sshfs-sidecar -n <tenant-namespace>
```

## SSH サーバー側の推奨設定

SFTP + ChrootDirectory を使用する場合：

```bash
Match User appuser
  ChrootDirectory /exports/appuser
  ForceCommand internal-sftp
  X11Forwarding no
  AllowTCPForwarding no
```

これにより chroot 内のパスのみにアクセス可能になります。

## よくある問題

### "error in libcrypto" が出た

**原因:** Secret を `--from-literal` で作成した場合、末尾改行が失われます。

**解決:**
```bash
kubectl delete secret ssh-key -n <tenant-namespace>
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/sshfs_key \
  -n <tenant-namespace>
```

### Pod が ContainerCreating で止まっている

**確認:**
```bash
kubectl describe pod <pod-name> -n <tenant-namespace>
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver
```

通常は CSI ドライバーがノード上で起動していないか、volumeAttributes に誤りがあります。

### マウント後もファイルが見えない

**確認:**
```bash
kubectl exec <pod-name> -c app -n <tenant-namespace> -- ls -la /data
kubectl exec <pod-name> -c sshfs-sidecar -n <tenant-namespace> -- mount | grep /data
```

remotePath が SSH サーバー側で存在し、ユーザーがアクセス可能か確認してください。

## 参考

- メインの README: [`../../../README.md`](../../../README.md)
- CSI ドライバー実装: [`../../../fuse-csi-driver`](../../../fuse-csi-driver)
- sshfs-sidecar 実装: [`../../../sshfs-sidecar`](../../../sshfs-sidecar)
