#!/bin/bash
# =============================================================================
# entrypoint.sh: s3fs サイドカーコンテナのエントリポイント
#
# 参考: https://github.com/pfnet-research/meta-fuse-csi-plugin/examples/proxy/
#
# 動作の流れ:
#   1. S3認証情報を passwd-s3fs ファイルに書き出す
#   2. `touch /dev/fuse` で libfuse に fusermount3 経由パスを強制する
#      → これがないと libfuse は /dev/fuse を直接 open しようとして失敗する
#   3. s3fs をフォアグラウンドで起動 (-f フラグ)
#   4. wait -n でプロセスが終了したら全体を終了
# =============================================================================

# 環境変数:
#   S3FS_BUCKET         - S3バケット名       (必須)
#   S3FS_MOUNT_POINT    - マウント先パス     (デフォルト: /mnt/s3fs)
#   S3FS_ENDPOINT       - S3エンドポイントURL (必須、例: http://minio.default:9000)
#   S3FS_REGION         - リージョン         (デフォルト: us-east-1)
#   AWS_ACCESS_KEY_ID   - アクセスキー       (Secretから注入)
#   AWS_SECRET_ACCESS_KEY - シークレットキー  (Secretから注入)
#   S3FS_OPTS           - 追加のs3fsオプション (デフォルト: 空)

S3FS_BUCKET="${S3FS_BUCKET}"
S3FS_MOUNT_POINT="${S3FS_MOUNT_POINT:-/mnt/s3fs}"
S3FS_ENDPOINT="${S3FS_ENDPOINT}"
S3FS_REGION="${S3FS_REGION:-us-east-1}"
S3FS_OPTS="${S3FS_OPTS:-}"

# -----------------------------------------------------------------------
# バケット名とエンドポイントの必須チェック
# -----------------------------------------------------------------------
if [ -z "$S3FS_BUCKET" ]; then
    echo "[ERROR] S3FS_BUCKET 環境変数が設定されていません。"
    exit 1
fi

if [ -z "$S3FS_ENDPOINT" ]; then
    echo "[ERROR] S3FS_ENDPOINT 環境変数が設定されていません。"
    exit 1
fi

if [ -z "$AWS_ACCESS_KEY_ID" ] || [ -z "$AWS_SECRET_ACCESS_KEY" ]; then
    echo "[ERROR] AWS_ACCESS_KEY_ID または AWS_SECRET_ACCESS_KEY が設定されていません。"
    exit 1
fi

# -----------------------------------------------------------------------
# 重要: touch /dev/fuse
#
# libfuse がマウント時に /dev/fuse を open() しようとするが、
# コンテナ内では権限エラーになる。
# /dev/fuse ファイルが存在すると libfuse は代わりに fusermount3 を呼び出す。
# fusermount3 は fusermount3-proxy に差し替え済みなので、
# CSI DaemonSet に処理を委譲できる。
# -----------------------------------------------------------------------
touch /dev/fuse

# -----------------------------------------------------------------------
# S3認証情報を passwd-s3fs ファイルに書き出し
# フォーマット: ACCESS_KEY_ID:SECRET_ACCESS_KEY
# -----------------------------------------------------------------------
echo "[INFO] S3認証情報を設定しています..."
echo "${AWS_ACCESS_KEY_ID}:${AWS_SECRET_ACCESS_KEY}" > /etc/passwd-s3fs
chmod 600 /etc/passwd-s3fs

# -----------------------------------------------------------------------
# マウントポイントディレクトリを作成
# -----------------------------------------------------------------------
mkdir -p "${S3FS_MOUNT_POINT}"

# -----------------------------------------------------------------------
# s3fs をフォアグラウンドで起動 (-f)
# -----------------------------------------------------------------------
echo "[INFO] s3fs を起動します: ${S3FS_BUCKET} -> ${S3FS_MOUNT_POINT}"
echo "[INFO] エンドポイント: ${S3FS_ENDPOINT}"
echo "[INFO] リージョン: ${S3FS_REGION}"

/usr/bin/s3fs \
    "${S3FS_BUCKET}" \
    "${S3FS_MOUNT_POINT}" \
    -o passwd_file=/etc/passwd-s3fs \
    -o url="${S3FS_ENDPOINT}" \
    -o endpoint="${S3FS_REGION}" \
    -o use_path_request_style \
    -o no_check_certificate \
    ${S3FS_OPTS} \
    -f \
    &

# いずれかのバックグラウンドプロセスが終了したらコンテナも終了
wait -n
exit $?
