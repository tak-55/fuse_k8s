FROM golang:1.21 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /receiver ./receiver/
RUN CGO_ENABLED=0 GOOS=linux go build -o /fusermount3-stub ./fusermount3-stub/

FROM ubuntu:22.04
RUN apt-get update && apt-get install -y \
    s3fs \
    fuse3 \
    && rm -rf /var/lib/apt/lists/*
RUN echo "user_allow_other" >> /etc/fuse.conf
RUN mkdir -p /mnt/fuse && chmod 777 /mnt/fuse
# uid 1000 のユーザーを作成（Kyverno が runAsUser: 1000 を注入するため）
RUN useradd -u 1000 -m -s /sbin/nologin appuser

COPY --from=builder /receiver /bin/receiver
# 本物の fusermount3 を stub に置き換える。
# stub は mount を実行せず、FUSE_PREOPEN_FD の fd を libfuse に返すだけ。
COPY --from=builder /fusermount3-stub /usr/bin/fusermount3
RUN ln -sf /usr/bin/fusermount3 /usr/bin/fusermount
RUN chmod 4755 /usr/bin/fusermount3
RUN chmod 4755 /usr/bin/fusermount
ENTRYPOINT ["/bin/receiver"]
