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

## 4. イメージのビルドと kind へのロード

```bash
# リポジトリルートに移動
cd /path/to/fuse_k8s

# CSI ドライバーイメージビルド
docker build -t fuse-csi-driver:latest ./fuse-csi-driver/

# サイドカーイメージビルド（fd-passing 用）
docker build -t sshfs-sidecar:latest ./sshfs-sidecar/
docker build -t s3fs-sidecar:latest ./s3fs-sidecar/

# kind クラスターへロード
kind load docker-image fuse-csi-driver:latest --name fuse-dev
kind load docker-image sshfs-sidecar:latest --name fuse-dev
kind load docker-image s3fs-sidecar:latest --name fuse-dev

# ロード確認
docker exec fuse-dev-control-plane crictl images | grep -E "fuse-csi-driver|sshfs-sidecar|s3fs-sidecar"
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
# Capsule v0.10.x では ClusterRole 名が capsule-namespace-provisioner に変更
kubectl create clusterrolebinding test-user-capsule \
  --clusterrole=capsule-namespace-provisioner \
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
# example-user として namespace を作成
kubectl create namespace oil-test --as=example-user
```

> **注意（Capsule v0.10.x + kind）**: `--as` 指定での namespace 作成では Capsule webhook が
> PSA ラベルを自動付与しない場合がある。ラベルが付かない場合は手動で付与する:

```bash
# ラベル確認
kubectl get namespace oil-test --show-labels

# ラベルが付いていない場合は手動で付与
kubectl label namespace oil-test \
  capsule.clastix.io/tenant=example-tenant \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=baseline \
  pod-security.kubernetes.io/warn=baseline

# 確認
kubectl get namespace oil-test --show-labels
```

期待結果:

```
NAME       STATUS   LABELS
oil-test   Active   capsule.clastix.io/tenant=example-tenant,
                    pod-security.kubernetes.io/enforce=restricted,
                    pod-security.kubernetes.io/audit=baseline,
                    pod-security.kubernetes.io/warn=baseline,...
```

---

## 7.1 Capsule ポリシー強制テスト

Capsule が PSS restricted を正しく強制しているか確認する。

```bash
# テスト 1: privileged Pod の作成が拒否されること
kubectl apply -n oil-test -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: privileged-test
spec:
  containers:
  - name: c
    image: busybox
    securityContext:
      privileged: true
    command: ["sleep", "3600"]
EOF
```

期待結果: `Error from server (Forbidden): ... violates PodSecurity "restricted"` でデプロイ拒否

```bash
# テスト 2: ホストネットワーク Pod の作成が拒否されること
kubectl apply -n oil-test -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: hostnet-test
spec:
  hostNetwork: true
  containers:
  - name: c
    image: busybox
    command: ["sleep", "3600"]
EOF
```

期待結果: 同様に拒否される

```bash
# テスト 3: 同じ Pod が管理者 namespace（default）には作成できること
# （PSA restricted の適用範囲が oil-test のみであることを確認）
kubectl run privileged-test --image=busybox \
  --overrides='{"spec":{"containers":[{"name":"c","image":"busybox","securityContext":{"privileged":true},"command":["sleep","3600"]}]}}' \
  -n default
kubectl delete pod privileged-test -n default
```

---

## 7.2 Kyverno ミューテーション単体テスト

sshfs Pod をデプロイする前に、Kyverno のミューテーションが正しく動作するか確認する。

```bash
# 最小 Pod（securityContext なし）を作成
kubectl apply -n oil-test -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: mutation-test
spec:
  containers:
  - name: app
    image: busybox
    command: ["sleep", "3600"]
  - name: sidecar
    image: busybox
    command: ["sleep", "3600"]
EOF

# Kyverno によって自動注入されたフィールドを確認
kubectl get pod mutation-test -n oil-test -o yaml
```

確認項目:

```bash
# 1. spec レベル: hostUsers: false
kubectl get pod mutation-test -n oil-test \
  -o jsonpath='{.spec.hostUsers}' && echo
# 期待: false

# 2. spec.securityContext: runAsNonRoot/runAsUser/seccompProfile
kubectl get pod mutation-test -n oil-test \
  -o jsonpath='{.spec.securityContext}' | python3 -m json.tool
# 期待:
# {
#   "runAsNonRoot": true,
#   "runAsUser": 1000,
#   "seccompProfile": {"type": "RuntimeDefault"}
# }

# 3. 各コンテナ（app・sidecar）の securityContext
# fd-passing アーキテクチャの核心: サイドカーも非特権であること
kubectl get pod mutation-test -n oil-test \
  -o jsonpath='{range .spec.containers[*]}{.name}{": privileged="}{.securityContext.privileged}{"\n"}{end}'
# 期待: app: privileged=<none>  sidecar: privileged=<none>
# （いずれも privileged: true でないこと）

# クリーンアップ
kubectl delete pod mutation-test -n oil-test
```

---

## 8. SSH 秘密鍵の準備（sshfs テスト用）

テスト用に SSH キーペアを作成する。外部 SSH サーバーが使えない場合は簡易 SSHD を kind 内に立てる。

### パターン A: 外部 SSH サーバーを使う場合

```bash
# 接続先確認
ssh -i ~/.ssh/sshfs_key your-user@your-ssh-host "echo OK"

# Secret 作成
# 注意: --from-file を使うこと。--from-literal は末尾改行が失われ SSH 認証エラーになる
kubectl create secret generic ssh-key \
  --from-file=private_key=~/.ssh/sshfs_key \
  -n oil-test
```

### パターン B: kind 内に簡易 SSHD を立てる

SSHD は root で動作する必要があるため、PSS-restricted の `oil-test` ではなく
`default` namespace（または `fuse-csi-system`）に立てる。

```bash
# テスト用 SSH 鍵ペア作成
ssh-keygen -t ed25519 -f /tmp/sshfs-test-key -N ""
PUBKEY=$(cat /tmp/sshfs-test-key.pub)

# OpenSSH + SFTP chroot の SSHD Pod を default namespace に起動
# （root で動作するため PSS-restricted の oil-test には置かない）
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: sshd-test
  namespace: default
spec:
  containers:
  - name: sshd
    image: ubuntu:22.04
    command:
    - /bin/bash
    - -c
    - |
      apt-get update -q && apt-get install -y -q openssh-server
      mkdir -p /var/run/sshd
      # testuser 作成（ログインシェルなし、パスワード '*' でロック解除）
      useradd -M -s /bin/false -p '*' testuser
      # SFTP chroot 用ディレクトリ（/ が chroot root、/upload が書き込み先）
      mkdir -p /upload && chown testuser:testuser /upload
      # 公開鍵設定
      mkdir -p /home/testuser/.ssh
      echo "\$PUBKEY" > /home/testuser/.ssh/authorized_keys
      chmod 700 /home/testuser/.ssh && chmod 600 /home/testuser/.ssh/authorized_keys
      chown -R testuser:testuser /home/testuser/.ssh
      # SFTP chroot 設定を sshd_config に追加
      cat >> /etc/ssh/sshd_config <<'SSHD_CONF'
      Match User testuser
        ChrootDirectory /
        ForceCommand internal-sftp
        X11Forwarding no
        AllowTcpForwarding no
      SSHD_CONF
      exec /usr/sbin/sshd -D -e
    env:
    - name: PUBKEY
      value: "$PUBKEY"
    ports:
    - containerPort: 22
EOF

# SSHD Pod の起動待ち
kubectl wait --for=condition=Ready pod/sshd-test -n default --timeout=120s

# SSHD の ClusterIP を取得
kubectl expose pod sshd-test -n default --port=22 --name=sshd-svc
SSHD_IP=$(kubectl get svc sshd-svc -n default -o jsonpath='{.spec.clusterIP}')
echo "SSHD IP: $SSHD_IP"

# 接続テスト（CSI driver から sshfs でマウントする前に動作確認）
# kind ノードのコンテナに入って確認
docker exec fuse-dev-control-plane \
  ssh -i /dev/stdin -o StrictHostKeyChecking=accept-new \
  testuser@$SSHD_IP echo "SSH OK" < /tmp/sshfs-test-key
```

**sshfs の設定値:**
- `host`: `$SSHD_IP`（上記コマンドで確認）
- `user`: `testuser`
- `remotePath`: `/upload`（chroot 内の書き込み可能ディレクトリ）
- `port`: `22`

```bash
# Secret 作成（/tmp/sshfs-test-key を使用）
kubectl create secret generic ssh-key \
  --from-file=private_key=/tmp/sshfs-test-key \
  -n oil-test
```

---

## 9. sshfs テスト Pod のデプロイと確認

> **kind 環境と `hostUsers: false` の対応状況**:
>
> | kind の実行環境 | `hostUsers: false` | 備考 |
> |---|---|---|
> | macOS Docker Desktop | ❌ | VM 経由でカーネルの user namespace に制約 |
> | macOS devcontainer (DooD) | ❌ | 同上（Docker Desktop VM の制約） |
> | Linux ネイティブ | ✅ | ホストカーネルが user namespace を直接サポート |
>
> macOS 環境では `mount-product-files.sh: permission denied` エラーが発生します。
>
> - **macOS kind テスト**: Kyverno ミューテーションの確認 + `default` namespace でマウント機能のみ確認
> - **`hostUsers: false` + PSS restricted の完全テスト**: Linux 上の kind または k3s が必要
>   → [docs/guide-k8s-setup-and-test.md](guide-k8s-setup-and-test.md) を参照

```bash
# fd-passing 用マニフェストをコピーして編集
cp sshfs/deploy-kind-fdpass.yaml /tmp/sshfs-test.yaml

# host / user / remotePath を実環境に合わせて編集
vi /tmp/sshfs-test.yaml

# Pod デプロイ（kind では default namespace を使用）
kubectl apply -f /tmp/sshfs-test.yaml -n default

# Ready になるまで待機
kubectl wait --for=condition=Ready pod/sshfs-fdpass-example -n default --timeout=60s
```

---

## 10. 動作確認テスト

### 10-1. Kyverno ミューテーション確認（oil-test namespace）

Kyverno ポリシーの確認は `oil-test` namespace でテスト Pod を使って行う（ステップ 7.2 参照）。

```bash
# ミューテーション確認用の一時 Pod を作成して確認
kubectl run mutation-verify --image=busybox -n oil-test -- sleep 60
kubectl get pod mutation-verify -n oil-test \
  -o jsonpath='hostUsers={.spec.hostUsers}{"\n"}runAsNonRoot={.spec.securityContext.runAsNonRoot}{"\n"}'
kubectl delete pod mutation-verify -n oil-test
```

期待結果:
```
hostUsers=false
runAsNonRoot=true
```

> **注意**: `hostUsers: false` が注入された Pod は kind on macOS では起動しない（制約）。
> ミューテーションが正しく機能していることの確認のみ行う。

### 10-2. FUSE マウント確認（default namespace）

kind では PSS/Kyverno なしの `default` namespace で FUSE マウント機能を確認する。

```bash
kubectl exec -n default sshfs-fdpass-example -- mount | grep fuse
```

期待結果: `fuse.sshfs on /data type fuse (rw,...)` が表示される

### 10-3. UID 確認と書き込みテスト

```bash
kubectl exec -n default sshfs-fdpass-example -- sh -c '
  echo "=== 実行UID ==="
  id
  echo "=== 書き込みテスト ==="
  echo "test $(date)" > /data/fuse-write-test.txt
  cat /data/fuse-write-test.txt
  echo "=== パーミッション確認 ==="
  ls -la /data/fuse-write-test.txt
'
```

> kind では Kyverno が適用されないため uid=0 で動作する。
> uid=1000 + hostUsers: false の組み合わせは k3s 環境で確認する。

### 10-4. PSS restricted 違反がないことを確認（oil-test）

```bash
kubectl get events -n oil-test | grep -i "policy\|forbidden\|violation"
```

期待結果: PSS 関連のエラーイベントなし（`hostUsers: false` 起因のエラーを除く）

### 10-5. CSI driver ログ確認

```bash
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver -c fuse-csi-driver --tail=20
```

期待結果: `NodePublishVolume: targetPath=... type=sshfs` のログが出力される

### 10-6. サイドカーログ確認

```bash
kubectl logs -n default sshfs-fdpass-example -c sshfs-sidecar
```

期待結果:
```
params 読み込み完了: host=... user=... remotePath=... port=...
UDS 接続完了
fusefd=N 受信完了
sshfs 起動: ...
```

---

## 11. クリーンアップ

```bash
# テスト Pod 削除（default namespace）
kubectl delete pod sshfs-fdpass-example -n default 2>/dev/null || true

# SSHD テストサーバー削除（パターン B を使った場合）
kubectl delete pod sshd-test -n default
kubectl delete svc sshd-svc -n default

# Tenant 削除
kubectl delete -f policy/capsule-tenant-example.yaml

# クラスター削除（全リソースまとめて削除）
kind delete cluster --name fuse-dev
```

---

## トラブルシューティング

### Pod が Pending のまま

```bash
kubectl describe pod sshfs-fdpass-example -n oil-test
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
