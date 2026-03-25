#!/bin/bash
# fd-passing アーキテクチャの kind 結合テストスクリプト
set -euo pipefail

CLUSTER_NAME="fuse-dev"
NAMESPACE="default"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# ==============================
# Step 1: kind クラスター起動
# ==============================
echo "=== [1/7] kind クラスター確認/起動 ==="
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
  echo "クラスター '${CLUSTER_NAME}' は既に起動中"
else
  kind create cluster --config "${REPO_ROOT}/.devcontainer/kind-config.yaml"
  echo "クラスター '${CLUSTER_NAME}' 起動完了"
fi
kubectl cluster-info --context "kind-${CLUSTER_NAME}"

# ==============================
# Step 2: イメージビルド + kind ロード
# ==============================
echo ""
echo "=== [2/7] Docker イメージビルド ==="
docker build -t fuse-csi-driver:latest "${REPO_ROOT}/fuse-csi-driver/"
docker build -t sshfs-sidecar:latest   "${REPO_ROOT}/sshfs-sidecar/"
docker build -t s3fs-sidecar:latest    "${REPO_ROOT}/s3fs-sidecar/"

echo ""
echo "=== [3/7] kind へイメージロード ==="
kind load docker-image fuse-csi-driver:latest --name "${CLUSTER_NAME}"
kind load docker-image sshfs-sidecar:latest   --name "${CLUSTER_NAME}"
kind load docker-image s3fs-sidecar:latest    --name "${CLUSTER_NAME}"

# ==============================
# Step 3: CSI ドライバーデプロイ
# ==============================
echo ""
echo "=== [4/7] CSI ドライバーデプロイ ==="
kubectl apply -f "${REPO_ROOT}/csi/fuse-csi-driver.yaml"
kubectl apply -f "${REPO_ROOT}/csi/fuse-csi-driver-daemonset.yaml"
kubectl rollout status daemonset/fuse-csi-driver -n fuse-csi-system --timeout=120s

# ==============================
# Step 4: MinIO（S3 互換）デプロイ（s3fs テスト用）
# ==============================
echo ""
echo "=== [5/7] MinIO デプロイ（S3テスト用）==="
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: minio
  namespace: default
  labels:
    app: minio
spec:
  containers:
    - name: minio
      image: minio/minio:latest
      args: ["server", "/data"]
      env:
        - name: MINIO_ROOT_USER
          value: "minioadmin"
        - name: MINIO_ROOT_PASSWORD
          value: "minioadmin"
      ports:
        - containerPort: 9000
---
apiVersion: v1
kind: Service
metadata:
  name: minio
  namespace: default
spec:
  selector:
    app: minio
  ports:
    - port: 9000
      targetPort: 9000
EOF
kubectl wait --for=condition=Ready pod/minio -n default --timeout=60s

# MinIO にバケット作成
kubectl exec minio -- sh -c "
  mc alias set local http://localhost:9000 minioadmin minioadmin
  mc mb local/test-bucket || true
  echo 'hello from s3fs' | mc pipe local/test-bucket/hello.txt
"

# ==============================
# Step 5: s3fs テスト
# ==============================
echo ""
echo "=== [6/7] s3fs fd-passing テスト ==="

# Secret 作成
kubectl create secret generic s3-credentials \
  --from-literal=access_key=minioadmin \
  --from-literal=secret_key=minioadmin \
  -n "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

# Pod デプロイ（MinIO エンドポイントに合わせて上書き）
cat "${REPO_ROOT}/s3fs/deploy-kind-fdpass.yaml" \
  | sed 's|your-bucket|test-bucket|g' \
  | sed 's|http://your-s3-endpoint:9000|http://minio.default.svc.cluster.local:9000|g' \
  | sed 's|noCheckCert: "false"|noCheckCert: "true"|g' \
  | kubectl apply -n "${NAMESPACE}" -f -

echo "s3fs Pod 起動待ち（最大120秒）..."
kubectl wait --for=condition=Ready pod/s3fs-fdpass-example -n "${NAMESPACE}" --timeout=120s || {
  echo "=== Pod 起動失敗 - ログ確認 ==="
  kubectl describe pod s3fs-fdpass-example -n "${NAMESPACE}"
  kubectl logs s3fs-fdpass-example -c s3fs-sidecar -n "${NAMESPACE}" || true
  exit 1
}

echo "--- マウント確認 ---"
kubectl exec s3fs-fdpass-example -c app -n "${NAMESPACE}" -- mount | grep fuse || true
echo "--- ファイル確認 ---"
kubectl exec s3fs-fdpass-example -c app -n "${NAMESPACE}" -- ls -la /data/
echo "--- 書き込みテスト ---"
kubectl exec s3fs-fdpass-example -c app -n "${NAMESPACE}" -- \
  sh -c "echo 'fd-pass write $(date)' > /data/fdpass-test.txt && cat /data/fdpass-test.txt"

# ==============================
# Step 6: ログ確認
# ==============================
echo ""
echo "=== [7/7] CSI DaemonSet ログ（fusefd 関連）==="
kubectl logs -n fuse-csi-system -l app=fuse-csi-driver --tail=20 | grep -E "fusefd|UDS|s3fs|FUSE" || \
  kubectl logs -n fuse-csi-system -l app=fuse-csi-driver --tail=20

echo ""
echo "=== テスト完了 ==="

# ==============================
# クリーンアップ（任意）
# ==============================
read -p "クラスターを削除しますか？ [y/N] " yn
if [[ "${yn}" == "y" || "${yn}" == "Y" ]]; then
  kind delete cluster --name "${CLUSTER_NAME}"
  echo "クラスター削除完了"
fi
