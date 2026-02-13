# =============================================================================
# Dockerfile: meta-fuse-csi-plugin / s3fs サイドカーイメージ
#
# 参考: https://github.com/pfnet-research/meta-fuse-csi-plugin
#
# fusermount3-proxy アプローチ:
#   s3fs は libfuse を使うため fusermount を呼び出す。
#   その fusermount3 を fusermount3-proxy に差し替えることで、
#   CSI DaemonSet Pod へマウント操作を委譲し、
#   ユーザー Pod は CAP_SYS_ADMIN 不要でマウント可能になる。
#
#   ポイント: entrypoint 内で `touch /dev/fuse` を実行することで
#   libfuse が fusermount3 を使うパスに誘導される。
#   (詳細は meta-fuse-csi-plugin の README 参照)
# =============================================================================

# -----------------------------------------------------------------------
# Stage 1: meta-fuse-csi-plugin ソースから fusermount3-proxy をビルド
#   - ビルド済みイメージが存在しない場合はソースから直接ビルドする
#   - リポジトリをクローンまたは ADD . . でソースを渡す
# -----------------------------------------------------------------------
FROM golang:1.20.7 AS fusermount3-proxy-builder

WORKDIR /meta-fuse-csi-plugin

# ビルドコンテキストに meta-fuse-csi-plugin のソースを置く場合:
#   ADD . .
# または git clone する場合:
RUN apt-get update && apt-get install -y --no-install-recommends git make && \
    git clone --depth=1 https://github.com/pfnet-research/meta-fuse-csi-plugin.git . && \
    make fusermount3-proxy BINDIR=/bin

# -----------------------------------------------------------------------
# Stage 2: s3fs サイドカー本体
# -----------------------------------------------------------------------
FROM ubuntu:22.04

ARG TARGETARCH

RUN apt-get update && apt-get upgrade -y
RUN apt-get install -y ca-certificates wget \
    # libfuse2: fuse-starter アプローチ互換のため
    libfuse2 \
    # fuse3: s3fs が依存する libfuse を含む
    fuse3

# s3fs 本体
RUN apt-get install -y s3fs

# -----------------------------------------------------------------------
# fusermount3-proxy を fusermount3 として配置し、
# fusermount (fuse2互換) にもシンボリックリンクを張る
# -----------------------------------------------------------------------
COPY --from=fusermount3-proxy-builder /bin/fusermount3-proxy /bin/fusermount3
RUN ln -sf /bin/fusermount3 /bin/fusermount

# -----------------------------------------------------------------------
# entrypoint スクリプトをコピー
# -----------------------------------------------------------------------
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

CMD ["/entrypoint.sh"]
