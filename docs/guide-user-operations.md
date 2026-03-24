# 本番ユーザー利用手順書

fuse-csi-driver を使って sshfs / s3fs を Pod にマウントする手順。
管理者が CSI driver・Capsule・Kyverno を構築済みであることが前提。

## ユーザーが行うこと・行わないこと

| 作業 | ユーザー | 備考 |
|------|---------|------|
| Secret の作成 | **ユーザー自身** | テナント namespace に作成 |
| Pod マニフェストの作成 | **ユーザー自身** | securityContext は書かなくてよい |
| Pod のデプロイ | **ユーザー自身** | |
| CSI driver の管理 | 管理者 | ユーザー不可 |
| Tenant/ポリシーの変更 | 管理者 | |

---

## 前提確認

作業前に管理者から以下を確認する:

- 自分のテナント namespace 名（例: `oil-ns-1`）
- CSI driver が使える状態か: `kubectl get csidriver fuse.csi.fuse-k8s.io`
- Kyverno ポリシーが有効か: `kubectl get clusterpolicy`

---

## sshfs の利用手順

### Step 1: SSH 秘密鍵の準備

SSH でリモートサーバーに接続するための鍵ペアを用意する。

```bash
# 鍵ペア作成（まだ持っていない場合）
ssh-keygen -t ed25519 -f ~/.ssh/sshfs_key -N ""

# 公開鍵をリモートサーバーに登録
ssh-copy-id -i ~/.ssh/sshfs_key.pub your-user@your-ssh-host

# 接続テスト（これが成功することを確認）
ssh -i ~/.ssh/sshfs_key your-user@your-ssh-host "echo 接続OK"
```

### Step 2: Kubernetes Secret の作成

```bash
# Secret 作成（自分のテナント namespace に作成）
kubectl create secret generic ssh-key \
  --from-literal=private_key="$(cat ~/.ssh/sshfs_key)" \
  -n <自分のテナント namespace>

# 確認（内容はロードされないが、キーが登録されているか確認）
kubectl get secret ssh-key -n <テナント namespace>
```

### Step 3: Pod マニフェストの作成

以下をコピーして `my-sshfs-pod.yaml` として保存し、コメントの箇所を編集する。

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-sshfs-pod
  # namespace は自分のテナント namespace に変更
spec:
  containers:
    - name: app
      image: ubuntu:22.04
      # securityContext は書かなくてよい（Kyverno が自動設定する）
      command: ["/bin/sh", "-c"]
      args:
        - |
          echo "マウント確認:"
          ls -la /data
          sleep infinity
      volumeMounts:
        - name: sshfs-vol
          mountPath: /data   # Pod 内でアクセスするパス

  volumes:
    - name: sshfs-vol
      csi:
        driver: fuse.csi.fuse-k8s.io
        volumeAttributes:
          type: sshfs
          host: "your-ssh-host"          # SSH サーバーのホスト名/IP
          user: "your-user"              # SSH ユーザー名
          remotePath: "/remote/path"     # マウントしたいリモートのパス
          port: "22"                     # SSH ポート（デフォルト: 22）
          strictHostKeyCheck: "true"     # 本番は "true" 推奨
        nodePublishSecretRef:
          name: ssh-key                  # Step 2 で作った Secret 名

  terminationGracePeriodSeconds: 30
```

### Step 4: Pod のデプロイ

```bash
kubectl apply -f my-sshfs-pod.yaml -n <テナント namespace>

# 起動待ち
kubectl wait --for=condition=Ready pod/my-sshfs-pod \
  -n <テナント namespace> --timeout=60s

# ステータス確認
kubectl get pod my-sshfs-pod -n <テナント namespace>
```

### Step 5: 動作確認

```bash
# Pod に入って確認
kubectl exec -it my-sshfs-pod -n <テナント namespace> -- sh

# Pod 内で実行
id                    # uid=1000 であること
ls -la /data          # リモートのファイルが見える
echo "test" > /data/test.txt  # 書き込みテスト
cat /data/test.txt
```

---

## s3fs の利用手順

### Step 1: S3 認証情報の Secret 作成

```bash
kubectl create secret generic s3-credentials \
  --from-literal=access_key=<アクセスキー> \
  --from-literal=secret_key=<シークレットキー> \
  -n <テナント namespace>
```

### Step 2: Pod マニフェストの作成

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-s3fs-pod
spec:
  containers:
    - name: app
      image: ubuntu:22.04
      # securityContext は書かなくてよい（Kyverno が自動設定する）
      command: ["/bin/sh", "-c"]
      args:
        - |
          ls -la /data
          sleep infinity
      volumeMounts:
        - name: s3fs-vol
          mountPath: /data

  volumes:
    - name: s3fs-vol
      csi:
        driver: fuse.csi.fuse-k8s.io
        volumeAttributes:
          type: s3fs
          bucket: "your-bucket"                      # S3 バケット名
          endpoint: "https://s3.your-endpoint.com"   # S3 エンドポイント
          region: "ap-northeast-1"                   # リージョン
          noCheckCert: "false"                       # 自己署名証明書環境では "true"
        nodePublishSecretRef:
          name: s3-credentials

  terminationGracePeriodSeconds: 30
```

```bash
kubectl apply -f my-s3fs-pod.yaml -n <テナント namespace>
kubectl wait --for=condition=Ready pod/my-s3fs-pod \
  -n <テナント namespace> --timeout=60s
```

---

## Deployment での利用（長期稼働アプリ向け）

Pod 単体ではなく Deployment を使う場合の例（sshfs）:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
spec:
  replicas: 1   # FUSE マウントは基本的に単一 Pod 推奨（同一パスへの並列マウントに注意）
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
        - name: app
          image: ubuntu:22.04
          command: ["/bin/sh", "-c"]
          args:
            - |
              ls /data
              sleep infinity
          volumeMounts:
            - name: sshfs-vol
              mountPath: /data
      volumes:
        - name: sshfs-vol
          csi:
            driver: fuse.csi.fuse-k8s.io
            volumeAttributes:
              type: sshfs
              host: "your-ssh-host"
              user: "your-user"
              remotePath: "/remote/path"
              port: "22"
              strictHostKeyCheck: "true"
            nodePublishSecretRef:
              name: ssh-key
      terminationGracePeriodSeconds: 30
```

> **注意**: `replicas: 1` を推奨。複数レプリカの場合、各 Pod が独立してマウントを行う。
> 同一ファイルに複数 Pod から同時書き込みする場合はアプリ側での排他制御が必要。

---

## よくある質問

### Q: Pod が起動せず Pending のままになる

```bash
kubectl describe pod <pod-name> -n <テナント namespace>
```

`Events` セクションを確認する。

| エラーメッセージ | 原因 | 対処 |
|----------------|------|------|
| `MountVolume failed` | CSI driver の問題 | 管理者に連絡 |
| `secret "ssh-key" not found` | Secret が存在しない | Step 2 を再実行 |
| `volumeAttributes.host が未設定` | host が空 | マニフェストを確認 |

### Q: Pod は起動するが /data が空

SSH の接続先・パスを確認する:

```bash
# Pod の外から SSH 接続テスト
ssh -i ~/.ssh/sshfs_key your-user@your-ssh-host "ls /remote/path"
```

→ ファイルが存在する場合はマウント成功しているが空ディレクトリの可能性。

### Q: /data への書き込みが Permission denied になる

```bash
kubectl exec -it <pod-name> -n <テナント namespace> -- id
# uid=1000 であることを確認

# マウントオプション確認
kubectl exec -it <pod-name> -n <テナント namespace> -- \
  mount | grep fuse
# allow_other と umask=000 が付いているはず
```

→ リモートサーバー側のパーミッション（SSH の場合）や S3 バケットポリシーを確認する。

### Q: securityContext を書いたら Kyverno に上書きされた

Kyverno は `+(key): value` 構文（未設定時のみ）を使っているため、
**ユーザーが明示的に書いた値は上書きされない**。
ただし PSS restricted に違反する値（`privileged: true` 等）は設定できない。

### Q: SSH キーをローテーションしたい

```bash
# 古い Secret を削除して再作成
kubectl delete secret ssh-key -n <テナント namespace>
kubectl create secret generic ssh-key \
  --from-literal=private_key="$(cat ~/.ssh/new_sshfs_key)" \
  -n <テナント namespace>

# Pod を再起動（Secret は次のマウント時に反映）
kubectl delete pod <pod-name> -n <テナント namespace>
# Deployment の場合
kubectl rollout restart deployment/my-app -n <テナント namespace>
```

### Q: CSI DaemonSet が再起動したらマウントが消えた

これは既知の制約です（設計上ステートレス）。Pod を再起動すると自動復旧します:

```bash
kubectl delete pod <pod-name> -n <テナント namespace>
# Deployment の場合
kubectl rollout restart deployment/my-app -n <テナント namespace>
```

---

## 管理者への問い合わせが必要な場合

以下の状況では管理者に連絡してください:

- `kubectl get csidriver fuse.csi.fuse-k8s.io` が存在しない
- `kubectl get pods -n fuse-csi-system` で DaemonSet が `Running` でない
- テナント namespace の PSA ラベルがない
- Kyverno ポリシーが存在しない（`kubectl get clusterpolicy` で空）
- 新しいテナント namespace を作りたい
