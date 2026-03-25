FROM golang:1.21 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /receiver ./receiver/
RUN CGO_ENABLED=0 GOOS=linux go build -o /fusermount3-stub ./fusermount3-stub/

FROM ubuntu:22.04
RUN apt-get update && apt-get install -y \
    sshfs \
    fuse3 \
    && rm -rf /var/lib/apt/lists/*
RUN echo "user_allow_other" >> /etc/fuse.conf

COPY --from=builder /receiver /bin/receiver
COPY --from=builder /fusermount3-stub /usr/bin/fusermount3
ENTRYPOINT ["/bin/receiver"]
