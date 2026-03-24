# kind 構築 + テスト手順書

fuse-csi-driver を kind ローカル環境で構築・テストする手順。

## 前提条件

| ツール | バージョン | 確認コマンド |
|--------|-----------|-------------|
| Docker | 20.10+ | `docker version` |
| kind | v0.20+ | `kind version` |
| kubectl | v1.30+ | `kubectl version --client` |
| Helm | v3.12+ | `helm version` |
| Go | 1.21+ | `go version` |

---

## 1. kind クラスター作成

```bash
# クラスター作成
kind create cluster --name fuse-dev

# コンテキスト確認
kubectl cluster-info --context kind-fuse-dev
kubectl get nodes
```

期待結果: ノードが `Ready` になる

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
  --version 0.10.8

# 起動確認
kubectl wait --for=condition=Ready pod \
  -l app.kubernetes.io/name=capsule \
  -n capsule-system --timeout=120s
```

期待結果: `capsule-controller-manager-*` が `Running`

---

## 3. Kyverno v1.15.2 インストール

```bash
# Helm リポジトリ追加
helm repo add kyverno https://kyverno.github.io/kyverno/
helm repo update

# Kyverno インストール
helm install kyverno kyverno/kyverno \
  --namespace kyverno \
  --create-namespace \
  --version 3.4.2

# 起動確認
kubectl wait --for=condition=Ready pod \
  -l app.kubernetes.io/name=kyverno \
  -n kyverno --timeout=180s
```

> **バージョン対応**: Kyverno アプリバージョン v1.15.2 に対応する Helm chart バージョンは `helm search repo kyverno/kyverno --versions` で確認すること。

期待結果: `kyverno-admission-controller-*` 等が `Running`

---

## 4. fuse-csi-driver イメージのビルドと kind へのロード

```bash
# リポジトリルートに移動
cd /path/to/fuse_k8s

# イメージビルド
docker build -t fuse-csi-driver:latest ./fuse-csi-driver/

# kind クラスターへロード
kind load docker-image fuse-csi-driver:latest --name fuse-dev

# ロード確認
docker exec fuse-dev-control-plane crictl images | grep fuse-csi-driver
```

---

## 5. fuse-csi-driver のデプロイ

```bash
# Namespace + CSIDriver リソース
kubectl apply -f csi/fuse-csi-driver.yaml

# DaemonSet（kind 用）
kubectl apply -f csi/fuse-csi-driver-daemonset.yaml

# 起動確認
kubectl wait --for=condition=Ready pod \
  -l app=fuse-csi-driver \
  -n fuse-csi-system --timeout=120s

kubectl get pods -n fuse-csi-system
```

期待結果:

```
NAME                     READY   STATUS    RESTARTS
fuse-csi-driver-xxxxx    2/2     Running   0
```

CSI driver 登録確認:

```bash
kubectl get csidriver fuse.csi.fuse-k8s.io
```

---

## 6. Capsule Tenant + Kyverno ポリシーの適用

```bash
# Capsule ユーザーの作成（テスト用）
kubectl create clusterrolebinding test-user-capsule \
  --clusterrole=capsule:user \
  --user=example-user

# Tenant の作成
kubectl apply -f policy/capsule-tenant-example.yaml

# Kyverno ポリシーの適用
kubectl apply -f policy/kyverno-force-userns.yaml
kubectl apply -f policy/kyverno-force-securecontext.yaml

# Tenant 確認
kubectl get tenant
```

---

## 7. テナント namespace の作成

```bash
# example-user として namespace を作成（Capsule が PSA ラベルを自動付与）
kubectl create namespace oil-test --as=example-user

# PSA ラベルが付いていることを確認
kubectl get namespace oil-test -o yaml | grep pod-security
```

期待結果:

```yaml
pod-security.kubernetes.io/enforce: restricted
pod-security.kubernetes.io/audit: baseline
pod-security.kubernetes.io/warn: baseline
capsule.clastix.io/tenant: example-tenant
```

---

## 8. SSH 秘密鍵の準備（sshfs テスト用）

テスト用に SSH キーペアを作成する。外部 SSH サーバーが使えない場合は簡易 SSHD を kind 内に立てる。

### パターン A: 外部 SSH サーバーを使う場合

```bash
# 接続先確認
ssh -i ~/.ssh/sshfs_key your-user@your-ssh-host "echo OK"

# Secret 作成
kubectl create secret generic ssh-key \
  --from-literal=private_key="$(cat ~/.ssh/sshfs_key)" \
  -n oil-test
```

### パターン B: kind 内に簡易 SSHD を立てる

```bash
# OpenSSH サーバー Pod を起動
kubectl apply -n oil-test -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: sshd-test
spec:
  containers:
  - name: sshd
    image: lscr.io/linuxserver/openssh-server:latest
    env:
    - name: PUBLIC_KEY
      value: "ssh-ed25519 AAAA..."   # 自分の公開鍵に変更
    - name: USER_NAME
      value: testuser
    ports:
    - containerPort: 2222
EOF

# SSH サービス作成
kubectl expose pod sshd-test -n oil-test --port=22 --target-port=2222 --name=sshd-svc
SSHD_IP=$(kubectl get svc sshd-svc -n oil-test -o jsonpath='{.spec.clusterIP}')
echo "SSHD IP: $SSHD_IP"
```

---

## 9. sshfs テスト Pod のデプロイと確認

```bash
# sshfs/deploy-kind-no-sidecar.yaml をコピーして編集
cp sshfs/deploy-kind-no-sidecar.yaml /tmp/sshfs-test.yaml

# host / user / remotePath を実環境に合わせて編集
vi /tmp/sshfs-test.yaml

# Pod デプロイ
kubectl apply -f /tmp/sshfs-test.yaml -n oil-test

# Ready になるまで待機
kubectl wait --for=condition=Ready pod/sshfs-example -n oil-test --timeout=60s
```

---

## 10. 動作確認テスト

### 10-1. Kyverno ミューテーション確認

```bash
kubectl get pod sshfs-example -n oil-test -o yaml | \
  grep -A3 "hostUsers\|runAsNonRoot\|runAsUser\|seccompProfile"
```

期待結果:

```yaml
hostUsers: false
securityContext:
  runAsNonRoot: true
  runAsUser: 1000
  seccompProfile:
    type: RuntimeDefault
```

### 10-2. FUSE マウント確認

```bash
kubectl exec -n oil-test sshfs-example -- mount | grep fuse
```

期待結果: `fuse.sshfs on /data type fuse.sshfs` が表示される

### 10-3. UID 1000 からの書き込みテスト

```bash
kubectl exec -n oil-test sshfs-example -- sh -c '
  echo "=== 実行UID ==="
  id
  echo "=== 書き込みテスト ==="
  echo "test $(date)" > /data/fuse-write-test.txt
  cat /data/fuse-write-test.txt
  echo "=== パーミッション確認 ==="
  ls -la /data/fuse-write-test.txt
'
```

期待結果: `uid=1000` で書き込み成功

### 10-4. PSS restricted 違反がないことを確認

```bash
kubectl get events -n oil-test | grep -i "policy\|forbidden\|violation"
```

期待結果: PSS 関連のエラーイベントなし

### 10-5. CSI driver ログ確認

```bash
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver -c fuse-csi-driver --tail=20
```

期待結果: `NodePublishVolume: targetPath=... type=sshfs` のログが出力される

---

## 11. クリーンアップ

```bash
# テスト Pod 削除
kubectl delete pod sshfs-example -n oil-test

# Tenant 削除
kubectl delete -f policy/capsule-tenant-example.yaml

# クラスター削除
kind delete cluster --name fuse-dev
```

---

## トラブルシューティング

### Pod が Pending のまま

```bash
kubectl describe pod sshfs-example -n oil-test
```

→ `MountVolume.MountDevice failed` の場合: CSI driver が起動していない。`kubectl get pods -n fuse-csi-system` を確認。

### マウントが失敗する

```bash
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver -c fuse-csi-driver | grep -i "error\|failed"
```

→ `秘密鍵` 関連エラー: Secret の `private_key` キー名を確認。
→ `ssh: connect to host` エラー: SSH サーバーのホスト・ポートを確認。

### Kyverno ポリシーが適用されない

```bash
kubectl get clusterpolicy
kubectl describe clusterpolicy force-userns-for-tenant
```

→ namespace に `capsule.clastix.io/tenant` ラベルがあるか確認:
```bash
kubectl get ns oil-test --show-labels
```
