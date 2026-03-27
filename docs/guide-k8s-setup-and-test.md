# K8s 構築 + テスト手順書

Rancher v2.13.3 管理下の k3s v1.33.5+k3s1 (Ubuntu 24.04) に
fuse-csi-driver + Capsule + Kyverno を導入する手順。

## 前提条件

| コンポーネント | バージョン | 備考 |
|--------------|-----------|------|
| Ubuntu | 24.04 LTS | k3s ノード OS |
| k3s | v1.33.5+k3s1 | Rancher v2.13.3 で管理 |
| Cilium | 1.19 | Rancher が CNI として設定済み |
| Helm | v3.12+ | 管理端末にインストール済み |
| kubectl | v1.30+ | Rancher kubeconfig を取得済み |

### kubeconfig の取得

Rancher UI → クラスター → 右上「kubeconfig をダウンロード」→
`~/.kube/config` に保存

```bash
kubectl get nodes
# 期待: 全ノードが Ready
```

---

## 1. k3s ノードの事前確認

### FUSE カーネルモジュール確認

```bash
# 全ノードで実行
for node in $(kubectl get nodes -o name | sed 's/node\///'); do
  echo "=== $node ==="
  kubectl debug node/$node -it --image=busybox -- sh -c \
    "modprobe fuse 2>/dev/null; ls /dev/fuse && echo 'OK'"
done
```

### user namespace 有効確認（hostUsers: false に必要）

```bash
# 各ノードの SSH で確認
cat /proc/sys/kernel/unprivileged_userns_clone
# 期待: 1（Ubuntu 24.04 はデフォルト有効）
```

---

## 2. Capsule v0.10.8 インストール

```bash
# Helm リポジトリ追加
helm repo add projectcapsule https://projectcapsule.github.io/charts
helm repo update

# Capsule インストール
helm install capsule projectcapsule/capsule \
  --namespace capsule-system \
  --create-namespace \
  --version 0.10.8 \
  --set manager.options.forceTenantPrefix=false

# 起動確認（数十秒待つ）
kubectl wait --for=condition=Ready pod \
  -l app.kubernetes.io/name=capsule \
  -n capsule-system --timeout=180s

kubectl get pods -n capsule-system
```

期待結果:

```
NAME                                    READY   STATUS
capsule-controller-manager-xxxxx       1/1     Running
```

---

## 3. Kyverno v1.15.2 インストール

```bash
helm repo add kyverno https://kyverno.github.io/kyverno/
helm repo update

# Helm chart バージョン確認
helm search repo kyverno/kyverno --versions | head -5

# Kyverno インストール
helm install kyverno kyverno/kyverno \
  --namespace kyverno \
  --create-namespace \
  --version 3.4.2 \
  --set admissionController.replicas=3 \
  --set backgroundController.replicas=2 \
  --set cleanupController.replicas=2 \
  --set reportsController.replicas=2

# 起動確認（1〜2分かかる場合あり）
kubectl wait --for=condition=Ready pod \
  -l app.kubernetes.io/name=kyverno \
  -n kyverno --timeout=300s

kubectl get pods -n kyverno
```

> **注意**: 本番環境では `replicas` を複数に設定して HA 構成にすること。
> `--version 3.4.2` は Kyverno app v1.15.2 に対応する chart バージョン。
> `helm search repo kyverno/kyverno --versions` で最新対応バージョンを確認すること。

---

## 4. Cilium の動作確認

Rancher が Cilium 1.19 を CNI として設定済みのため、追加インストールは不要。

```bash
# Cilium の状態確認
kubectl get pods -n kube-system -l k8s-app=cilium

# Cilium CLI がある場合
cilium status
```

> **注意**: fuse-csi-driver の `hostPath` マウントは Cilium のネットワークポリシーとは独立している。
> ただし NodePort/Service を使う場合は Cilium の NetworkPolicy を確認すること。

---

## 5. fuse-csi-driver のデプロイ

ghcr.io のイメージを使用する（GitHub Actions で自動ビルド済み）。

### プライベートリポジトリの場合: imagePullSecret を作成

```bash
echo <GITHUB_PAT> | docker login ghcr.io -u <GITHUB_USERNAME> --password-stdin

kubectl create secret docker-registry ghcr-secret \
  --docker-server=ghcr.io \
  --docker-username=<GITHUB_USERNAME> \
  --docker-password=<GITHUB_PAT> \
  -n fuse-csi-system
```

### デプロイ

```bash
# Namespace + CSIDriver リソース
kubectl apply -f csi/fuse-csi-driver.yaml

# DaemonSet
kubectl apply -f csi/fuse-csi-driver-daemonset-prod.yaml

# 全ノードで DaemonSet が起動するまで待機
kubectl rollout status daemonset/fuse-csi-driver -n fuse-csi-system --timeout=300s

kubectl get pods -n fuse-csi-system -o wide
```

期待結果: 全ノード分の Pod が `2/2 Running`

### CSIDriver 登録確認

```bash
kubectl get csidriver fuse.csi.fuse-k8s.io -o yaml
```

---

## 6. Capsule Tenant + Kyverno ポリシーの適用

```bash
# Kyverno ポリシー適用
kubectl apply -f policy/kyverno-force-userns.yaml
kubectl apply -f policy/kyverno-force-securecontext.yaml

# ポリシー確認
kubectl get clusterpolicy
```

期待結果:

```
NAME                          ADMISSION   BACKGROUND   VALIDATE ACTION   READY   AGE
force-secure-context-to-pod   true        true         Audit             True    Xs
force-userns-for-tenant       true        true         Audit             True    Xs
```

Tenant の作成は管理者が行う（手順書「本番管理者管理手順書」を参照）。

---

## 7. テスト用 Tenant + namespace 作成

```bash
# テスト用 Tenant を適用
kubectl apply -f policy/capsule-tenant-example.yaml

# テスト namespace を example-user として作成
# （Capsule が PSA ラベルを自動付与）
kubectl create namespace oil-test --as=example-user

# PSA ラベル確認
kubectl get namespace oil-test --show-labels
```

期待結果:

```
NAME       pod-security.kubernetes.io/enforce   capsule.clastix.io/tenant
oil-test   restricted                           example-tenant
```

---

## 8. sshfs テスト

### Secret 作成

```bash
# 注意: --from-file を使うこと。--from-literal は末尾改行が失われ SSH 認証エラーになる
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/sshfs_key \
  -n oil-test
```

### Pod デプロイ

`sshfs/deploy-kind.yaml` をコピーして `host`/`user`/`remotePath` を編集:

```bash
cp sshfs/deploy-kind.yaml /tmp/sshfs-test.yaml
# host / user / remotePath を実環境の値に変更
# image: sshfs-sidecar:latest を ghcr.io のイメージに変更し imagePullPolicy: Always に変更
kubectl apply -f /tmp/sshfs-test.yaml -n oil-test
kubectl wait --for=condition=Ready pod/sshfs-fdpass-example -n oil-test --timeout=60s
```

### 確認テスト

```bash
# Kyverno ミューテーション確認
kubectl get pod sshfs-fdpass-example -n oil-test -o jsonpath='{.spec.hostUsers}' && echo
# 期待: false

kubectl get pod sshfs-fdpass-example -n oil-test \
  -o jsonpath='{.spec.securityContext}' | python3 -m json.tool
# 期待: runAsNonRoot:true, runAsUser:1000, seccompProfile:RuntimeDefault

# マウント確認
kubectl exec -n oil-test sshfs-fdpass-example -- mount | grep fuse
# 期待: fuse.sshfs が /data にマウントされている

# UID 1000 から書き込みテスト
kubectl exec -n oil-test sshfs-fdpass-example -- sh -c \
  'id && echo "k3s test $(hostname)" > /data/test.txt && cat /data/test.txt'
# 期待: uid=1000 で書き込み成功
```

---

## 9. s3fs テスト（MinIO 使用例）

```bash
# MinIO を kind に立てる場合（テスト用）
kubectl apply -n oil-test -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: minio-test
spec:
  containers:
  - name: minio
    image: quay.io/minio/minio:latest
    args: ["server", "/data"]
    env:
    - name: MINIO_ROOT_USER
      value: minioadmin
    - name: MINIO_ROOT_PASSWORD
      value: minioadmin
    ports:
    - containerPort: 9000
EOF

kubectl expose pod minio-test -n oil-test --port=9000 --name=minio-svc
MINIO_IP=$(kubectl get svc minio-svc -n oil-test -o jsonpath='{.spec.clusterIP}')

# バケット作成（mc CLI 必要）
mc alias set local http://$MINIO_IP:9000 minioadmin minioadmin
mc mb local/testbucket

# Secret 作成
kubectl create secret generic s3-credentials \
  --from-literal=access_key=minioadmin \
  --from-literal=secret_key=minioadmin \
  -n oil-test

# s3fs/deploy-kind.yaml の endpoint を $MINIO_IP:9000 に書き換えてデプロイ
```

---

## 10. クリーンアップ

```bash
kubectl delete pod sshfs-fdpass-example -n oil-test
kubectl delete namespace oil-test
kubectl delete -f policy/capsule-tenant-example.yaml
```

---

## トラブルシューティング

### DaemonSet が特定ノードで起動しない

```bash
kubectl describe pod <fuse-csi-pod> -n fuse-csi-system
```

→ `privileged containers are not allowed` の場合:
```bash
# fuse-csi-system namespace の PSA ラベルを確認
kubectl get namespace fuse-csi-system --show-labels
# pod-security.kubernetes.io/enforce: privileged が必要
kubectl label namespace fuse-csi-system \
  pod-security.kubernetes.io/enforce=privileged \
  pod-security.kubernetes.io/enforce-version=latest --overwrite
```

### k3s で /dev/fuse が存在しない

```bash
# ノードで確認
ls -la /dev/fuse
# 存在しない場合: カーネルモジュールロード
modprobe fuse
```

### DaemonSet が起動しない（ソケットパス関連）

`fuse-csi-driver-daemonset-prod.yaml` の kubelet パス（`/var/lib/kubelet`）が
実環境と一致しているか確認してください。

```bash
# 現在のマニフェストを確認
kubectl get daemonset fuse-csi-driver -n fuse-csi-system -o yaml | grep hostPath
```

### Cilium によるトラフィックブロック

```bash
# CSI driver → SSH サーバーへの接続確認
kubectl exec -n fuse-csi-system <csi-pod> -c fuse-csi-driver -- \
  nc -zv <ssh-host> 22
```

→ ブロックされている場合: Cilium NetworkPolicy を確認・調整
