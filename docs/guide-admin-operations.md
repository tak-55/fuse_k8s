# 本番管理者管理手順書

fuse-csi-driver + Capsule + Kyverno の本番環境における
管理者向け運用・管理手順。

## 管理者の役割と責任範囲

| 作業 | 担当 | 備考 |
|------|------|------|
| CSI DaemonSet のデプロイ・更新 | **管理者** | ユーザー不可 |
| Capsule Tenant の作成・削除 | **管理者** | |
| Kyverno ポリシーの管理 | **管理者** | |
| fuse-csi-system namespace の管理 | **管理者** | |
| テナント namespace の管理 | Tenant Owner（ユーザー代表） | Capsule 経由 |
| Secret の作成 | **ユーザー**（各自のテナント ns） | 管理者不要 |
| Pod のデプロイ | **ユーザー** | |

---

## Tenant の作成

ユーザーのプロジェクト（チーム）ごとに Capsule Tenant を作成する。

### テンプレートを使った Tenant 作成

```bash
cat <<EOF | kubectl apply -f -
apiVersion: capsule.clastix.io/v1beta2
kind: Tenant
metadata:
  name: <tenant-name>         # 例: oil-prod
spec:
  owners:
    - kind: User
      name: <owner-username>  # 例: alice
  namespaceOptions:
    additionalMetadata:
      labels:
        pod-security.kubernetes.io/enforce: restricted
        pod-security.kubernetes.io/audit: baseline
        pod-security.kubernetes.io/warn: baseline
        capsule.clastix.io/tenant: <tenant-name>
    quota: 5                  # namespace 数の上限
  resourceQuotas:
    items:
      - hard:
          requests.cpu: "10"
          requests.memory: 20Gi
          limits.cpu: "20"
          limits.memory: 40Gi
          pods: "50"
EOF
```

### Tenant 一覧確認

```bash
kubectl get tenant
kubectl get tenant <tenant-name> -o yaml
```

### Tenant の削除

```bash
# Tenant を削除すると配下の namespace も削除される
kubectl delete tenant <tenant-name>
```

---

## Kyverno ポリシーの管理

### ポリシーの状態確認

```bash
kubectl get clusterpolicy
kubectl describe clusterpolicy force-userns-for-tenant
kubectl describe clusterpolicy force-secure-context-to-pod
```

### ポリシーの動作検証（特定 namespace で）

```bash
# テスト Pod を作成してミューテーションを確認
kubectl run verify-mutation --image=busybox -n <tenant-ns> -- sleep 3600
kubectl get pod verify-mutation -n <tenant-ns> -o yaml | \
  grep -A5 "hostUsers\|runAsNonRoot\|seccompProfile"
kubectl delete pod verify-mutation -n <tenant-ns>
```

### ポリシーの更新

```yaml
# policy/kyverno-force-securecontext.yaml を編集後
kubectl apply -f policy/kyverno-force-securecontext.yaml
```

> **注意**: ポリシー変更は既存 Pod には即時適用されない。次の Pod 作成時に反映される。

---

## fuse-csi-driver の更新

### イメージの更新（ローリングアップデート）

```bash
# 新しいイメージタグを指定して更新
kubectl set image daemonset/fuse-csi-driver \
  fuse-csi-driver=ghcr.io/tak-55/fuse_k8s-fuse-csi-driver:<new-tag> \
  -n fuse-csi-system

# ロールアウト状況を監視
kubectl rollout status daemonset/fuse-csi-driver -n fuse-csi-system

# 更新後の Pod 確認
kubectl get pods -n fuse-csi-system -o wide
```

> **注意**: DaemonSet の更新中、更新されたノードの既存 FUSE マウントが一時的に失われる。
> 該当ノードのユーザー Pod を再起動することで回復する。

### マニフェスト全体の再適用

```bash
kubectl apply -f csi/fuse-csi-driver.yaml
kubectl apply -f csi/fuse-csi-driver-daemonset-prod.yaml
```

---

## モニタリング

### CSI driver のログ確認

```bash
# 全ノードのリアルタイムログ
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver \
  -c fuse-csi-driver -f --max-log-requests=10

# 特定ノードのログ
NODE=<node-name>
POD=$(kubectl get pod -n fuse-csi-system \
  -l app=fuse-csi-driver \
  -o jsonpath="{.items[?(@.spec.nodeName=='$NODE')].metadata.name}")
kubectl logs -n fuse-csi-system $POD -c fuse-csi-driver --tail=100
```

### アクティブなマウント数の把握

各ノードで動作中の sshfs/s3fs プロセス数を確認:

```bash
for node in $(kubectl get nodes -o name | sed 's/node\///'); do
  echo "=== $node ==="
  kubectl debug node/$node -it --image=ubuntu -- \
    sh -c "ps aux | grep -c '[s]shfs\|[s]3fs'" 2>/dev/null
done
```

### 異常終了の検出

```bash
# Kyverno ポリシー違反イベント
kubectl get events --all-namespaces | grep -i "policy\|kyverno"

# CSI driver の Warning イベント
kubectl get events -n fuse-csi-system | grep Warning

# プロセスクラッシュログ（sshfs/s3fs の予期しない終了）
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver \
  -c fuse-csi-driver | grep -i "プロセス終了\|crash\|error"
```

---

## ユーザー Pod の強制停止とリソース回収

Pod 削除時に CSI driver が自動でアンマウント・クリーンアップを実行する。

```bash
# テナント namespace の Pod を全削除
kubectl delete pods --all -n <tenant-ns>

# 残留マウントがないか確認（ノードで実行）
mount | grep "fuse.sshfs\|fuse.s3fs"
```

残留マウントが存在する場合の強制アンマウント:

```bash
# 対象ノードで実行
# 標準 kubelet の場合
TARGET_PATH=/var/lib/kubelet/pods/<pod-uid>/volumes/...
# k3s の場合
TARGET_PATH=/var/lib/rancher/k3s/agent/kubelet/pods/<pod-uid>/volumes/...

fusermount3 -u $TARGET_PATH || umount $TARGET_PATH
```

---

## 証明書・Secret の管理方針

- SSH 秘密鍵・S3 認証情報は**ユーザーが各自のテナント namespace に作成**する
- 管理者は Secret の内容を管理しない（最小権限の原則）
- テナント namespace 削除時に Secret も自動削除される
- Secret のローテーションはユーザーが実施し、対応する Pod を再起動する

---

## CSI driver の障害対応

### DaemonSet Pod が CrashLoopBackOff

```bash
kubectl logs -n fuse-csi-system <pod-name> -c fuse-csi-driver --previous
```

よくある原因:
- `/dev/fuse` が存在しない → ノードで `modprobe fuse`
- ソケットファイルが残留 → 以下のパスを確認して削除

```bash
# ソケットファイルの残留を確認・削除（対象ノードで実行）

# 標準 kubelet（kubeadm / RKE2）の場合
ls -la /var/lib/kubelet/plugins/fuse.csi.fuse-k8s.io/
rm -f /var/lib/kubelet/plugins/fuse.csi.fuse-k8s.io/csi.sock

# k3s の場合（kubelet パスが異なる）
ls -la /var/lib/rancher/k3s/agent/kubelet/plugins/fuse.csi.fuse-k8s.io/
rm -f /var/lib/rancher/k3s/agent/kubelet/plugins/fuse.csi.fuse-k8s.io/csi.sock
```

### ユーザー Pod がマウントできない

```bash
# 1. CSI driver のログを確認
kubectl logs -n fuse-csi-system \
  $(kubectl get pod -n fuse-csi-system -l app=fuse-csi-driver \
    -o jsonpath="{.items[0].metadata.name}") \
  -c fuse-csi-driver | grep -A5 "NodePublishVolume"

# 2. Secret が正しいかを確認（管理者は内容を見ない、キーの存在のみ確認）
kubectl get secret <secret-name> -n <tenant-ns> -o jsonpath='{.data}' | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(list(d.keys()))"

# 3. CSIDriver リソースが正しいか確認
kubectl get csidriver fuse.csi.fuse-k8s.io -o yaml
```

---

## バックアップ・災害復旧

fuse-csi-driver は**ステートレス**（マウント情報はメモリ上のみ）。

- CSI driver 再起動後は既存マウントが失われる
- ユーザー Pod を再起動することで自動復旧する
- 設定は Git リポジトリで管理（`csi/`・`policy/` ディレクトリ）

復旧手順:

```bash
# 1. CSI driver 再デプロイ
kubectl apply -f csi/fuse-csi-driver.yaml
kubectl apply -f csi/fuse-csi-driver-daemonset-prod.yaml

# 2. 影響を受けたユーザー Pod を再起動
kubectl rollout restart deployment/<user-deployment> -n <tenant-ns>
# または Pod 削除（Deployment/StatefulSet の場合は自動再作成）
kubectl delete pods --all -n <tenant-ns>
```

---

## アップグレード手順（Capsule / Kyverno）

### Capsule アップグレード

```bash
helm upgrade capsule projectcapsule/capsule \
  -n capsule-system \
  --version <new-version> \
  --reuse-values

kubectl rollout status deployment/capsule-controller-manager -n capsule-system
```

### Kyverno アップグレード

```bash
# 事前: ポリシー設定をバックアップ
kubectl get clusterpolicy -o yaml > /tmp/kyverno-policies-backup.yaml

helm upgrade kyverno kyverno/kyverno \
  -n kyverno \
  --version <new-chart-version> \
  --reuse-values

kubectl rollout status deployment -n kyverno
```

> **注意**: Kyverno のメジャーバージョンアップ時は API バージョンの変更に注意。
> ポリシーファイル（`policy/`）の互換性を事前に確認すること。
