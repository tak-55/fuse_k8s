package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

type FuseParams struct {
	Type       string `json:"type"`
	Host       string `json:"host"`
	User       string `json:"user"`
	RemotePath string `json:"remotePath"`
	Port       string `json:"port"`
}

type FuseCreds struct {
	PrivateKey string `json:"private_key"`
}

func main() {
	paramsPath := "/fuse-fd/params.json"
	socketPath := "/fuse-fd/csi.sock"

	// 1. params.json が現れるまで待機（最大60秒）
	var params FuseParams
	for i := 0; i < 60; i++ {
		data, err := os.ReadFile(paramsPath)
		if err == nil {
			if err := json.Unmarshal(data, &params); err == nil && params.Host != "" {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if params.Host == "" {
		fmt.Fprintln(os.Stderr, "params.json 読み込みタイムアウト")
		os.Exit(1)
	}
	fmt.Printf("params 読み込み完了: host=%s user=%s remotePath=%s port=%s\n",
		params.Host, params.User, params.RemotePath, params.Port)

	// 2. UDS に接続（CSI DaemonSet が server）最大60秒リトライ
	var conn *net.UnixConn
	for i := 0; i < 60; i++ {
		c, err := net.Dial("unix", socketPath)
		if err == nil {
			conn = c.(*net.UnixConn)
			break
		}
		time.Sleep(time.Second)
	}
	if conn == nil {
		fmt.Fprintln(os.Stderr, "UDS 接続タイムアウト")
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Println("UDS 接続完了")

	// 3. credentials JSON + fd を SCM_RIGHTS で受信
	buf := make([]byte, 65535)
	oob := make([]byte, unix.CmsgSpace(4))
	n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fd 受信失敗: %v\n", err)
		os.Exit(1)
	}

	var creds FuseCreds
	if err := json.Unmarshal(buf[:n], &creds); err != nil {
		fmt.Fprintf(os.Stderr, "creds JSON parse 失敗: %v\n", err)
		os.Exit(1)
	}
	if creds.PrivateKey == "" {
		fmt.Fprintln(os.Stderr, "private_key が空です")
		os.Exit(1)
	}

	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) == 0 {
		fmt.Fprintln(os.Stderr, "SCM_RIGHTS parse 失敗")
		os.Exit(1)
	}
	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil || len(fds) == 0 {
		fmt.Fprintln(os.Stderr, "UnixRights parse 失敗")
		os.Exit(1)
	}
	fusefd := fds[0]
	fmt.Printf("fusefd=%d 受信完了\n", fusefd)

	// fusefd を子プロセス（sshfs → fusermount3-stub）に継承させるため CLOEXEC を外す
	unix.FcntlInt(uintptr(fusefd), unix.F_SETFD, 0) // clear FD_CLOEXEC

	// 4. SSH 秘密鍵を一時ファイルに書き出す
	keyFile, err := os.CreateTemp("", "sshfs-key-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "鍵ファイル作成失敗: %v\n", err)
		os.Exit(1)
	}
	keyFile.WriteString(creds.PrivateKey)
	keyFile.Chmod(0600)
	keyFile.Close()
	defer os.Remove(keyFile.Name())

	port := params.Port
	if port == "" {
		port = "22"
	}

	if err := os.MkdirAll("/mnt/fuse", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "/mnt/fuse 作成失敗: %v\n", err)
		os.Exit(1)
	}

	// 5. /dev/fuse が通常ファイルであることを確認（Dockerfile でビルド時に作成済み）
	// libfuse3 は stat("/dev/fuse") が成功しないと fusermount3 を呼ばずに終了する。
	// 通常ファイルとして存在することで stat は成功し、open 後の FUSE ioctl 失敗で
	// fusermount3-stub（= /usr/bin/fusermount3）にフォールバックする。
	// uid 1000 で実行中のため何もしない（Dockerfile で作成済み）。

	// 6. sshfs 起動
	// FUSE_PREOPEN_FD: fusermount3-stub が fusefd を libfuse に返すために使う
	args := []string{
		params.User + "@" + params.Host + ":" + params.RemotePath,
		"/mnt/fuse",
		"-p", port,
		"-o", "allow_other",
		"-o", "umask=000",
		"-o", "IdentityFile=" + keyFile.Name(),
		"-o", "StrictHostKeyChecking=accept-new",
		"-f",
	}

	fmt.Printf("sshfs 起動: %s@%s:%s\n", params.User, params.Host, params.RemotePath)
	cmd := exec.Command("sshfs", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), fmt.Sprintf("FUSE_PREOPEN_FD=%d", fusefd))

	// sshfs をバックグラウンドで起動（Start）し、プロセスが開始したら ready ファイルを書く。
	// readinessProbe がこのファイルを検出したとき kubelet はアプリコンテナを起動する。
	// sidecar initContainer として動作する場合、これにより FUSE サーブ開始後にアプリが起動する。
	readyPath := "/fuse-fd/ready"
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sshfs 起動失敗: %v\n", err)
		os.Exit(1)
	}
	// sshfs がプロセスとして起動したことを知らせる（SSH 接続確立前だが許容範囲）
	os.WriteFile(readyPath, []byte("ok\n"), 0644)
	fmt.Println("sshfs プロセス起動完了 → /fuse-fd/ready 書き込み済み")

	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "sshfs 終了: %v\n", err)
		os.Remove(readyPath)
		os.Exit(1)
	}
	os.Remove(readyPath)
}
