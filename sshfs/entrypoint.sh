#!/bin/bash
# =============================================================================
# entrypoint.sh: sshfs サイドカーコンテナのエントリポイント
#
# 参考: https://github.com/pfnet-research/meta-fuse-csi-plugin/examples/proxy/sshfs/
#
# 動作の流れ:
#   1. SSHサーバー (sshd) をバックグラウンドで起動 (接続先が別サーバーの場合は不要)
#   2. `touch /dev/fuse` で libfuse に fusermount3 経由パスを強制する
#      → これがないと libfuse は /dev/fuse を直接 open しようとして失敗する
#   3. sshfs をフォアグラウンドで起動 (-f フラグ)
#   4. wait -n でどちらかのプロセスが終了したら全体を終了
# =============================================================================

# 環境変数:
#   SSHFS_HOST          - 接続先SSHホスト (デフォルト: localhost)
#   SSHFS_USER          - SSHユーザー名  (デフォルト: root)
#   SSHFS_REMOTE_PATH   - リモートパス   (デフォルト: /root/sshfs-example)
#   SSHFS_MOUNT_POINT   - マウント先     (デフォルト: /tmp)
#   SSHFS_PORT          - SSHポート      (デフォルト: 22)
#   USE_LOCAL_SSHD      - "true" の場合コンテナ内 sshd を起動 (デフォルト: false)

SSHFS_HOST="${SSHFS_HOST:-localhost}"
SSHFS_USER="${SSHFS_USER:-root}"
SSHFS_REMOTE_PATH="${SSHFS_REMOTE_PATH:-/root/sshfs-example}"
SSHFS_MOUNT_POINT="${SSHFS_MOUNT_POINT:-/tmp}"
SSHFS_PORT="${SSHFS_PORT:-22}"
USE_LOCAL_SSHD="${USE_LOCAL_SSHD:-false}"

# -----------------------------------------------------------------------
# (オプション) ローカル SSHd の起動
# 接続先が外部SSHサーバーの場合は USE_LOCAL_SSHD=false のまま使用しない
# -----------------------------------------------------------------------
if [[ "${USE_LOCAL_SSHD}" == "true" ]]; then
    echo "[INFO] sshd を起動します..."
    /usr/sbin/sshd -D &
    # sshd の準備が整うまで待機
    sleep 1
fi

# -----------------------------------------------------------------------
# 重要: touch /dev/fuse
#
# libfuse3 がマウント時に /dev/fuse を open() しようとするが、
# コンテナ内では権限エラーになる。
# /dev/fuse ファイルが存在すると libfuse は代わりに fusermount3 を呼び出す。
# fusermount3 は fusermount3-proxy に差し替え済みなので、
# CSI DaemonSet に処理を委譲できる。
# -----------------------------------------------------------------------
touch /dev/fuse

# -----------------------------------------------------------------------
# (無効化) SSH秘密鍵を環境変数からファイルに出力
# 理由: 下記の sshfs コマンドでは -o IdentityFile=/secrets/ssh/private_key を使用しており
#       ここで書き出したファイル (/root/.ssh/private_key) は参照されないため。
# -----------------------------------------------------------------------
# if [ -n "$SSH_PRIVATE_KEY" ]; then
#     mkdir -p /root/.ssh
#     echo "$SSH_PRIVATE_KEY" > /root/.ssh/private_key
#     chmod 600 /root/.ssh/private_key
# fi

# -----------------------------------------------------------------------
# sshfs をフォアグラウンドで起動 (-f)
# -----------------------------------------------------------------------
echo "[INFO] sshfs を起動します: ${SSHFS_USER}@${SSHFS_HOST}:${SSHFS_REMOTE_PATH} -> ${SSHFS_MOUNT_POINT}"

/usr/bin/sshfs \
    "${SSHFS_USER}@${SSHFS_HOST}:${SSHFS_REMOTE_PATH}" \
    "${SSHFS_MOUNT_POINT}" \
    -p "${SSHFS_PORT}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o IdentityFile=/secrets/ssh/private_key \
    -f \
    &

# いずれかのバックグラウンドプロセスが終了したらコンテナも終了
wait -n
exit $?