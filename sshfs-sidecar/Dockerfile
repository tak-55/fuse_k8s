FROM golang:1.21 AS builder
WORKDIR /workspace
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /receiver ./receiver/

FROM ubuntu:22.04
RUN apt-get update && apt-get install -y \
    sshfs \
    fuse3 \
    && rm -rf /var/lib/apt/lists/*
RUN echo "user_allow_other" >> /etc/fuse.conf

COPY --from=builder /receiver /bin/receiver
ENTRYPOINT ["/bin/receiver"]
