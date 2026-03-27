# =============================================================================
# fuse-csi-driver Dockerfile
#
# Stage 1: Go バイナリのビルド
# Stage 2: 実行イメージ（sshfs + s3fs + fuse3 を含む）
# =============================================================================

# Stage 1: Go バイナリのビルド
FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /fuse-csi-driver ./cmd/

# Stage 2: 実行イメージ
FROM ubuntu:22.04
RUN apt-get update && apt-get install -y \
    sshfs \
    s3fs \
    fuse3 \
    && rm -rf /var/lib/apt/lists/*

# allow_other を有効化:
#   CSI DaemonSet (root) がマウントしたファイルに
#   ユーザーコンテナ (runAsNonRoot, UID 1000) からアクセスするために必要。
#   allow_other オプション使用の前提条件。
RUN echo "user_allow_other" >> /etc/fuse.conf

COPY --from=builder /fuse-csi-driver /bin/fuse-csi-driver

ENTRYPOINT ["/bin/fuse-csi-driver"]
