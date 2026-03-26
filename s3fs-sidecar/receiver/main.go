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
	Type        string `json:"type"`
	Bucket      string `json:"bucket"`
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	NoCheckCert string `json:"noCheckCert"`
}

type FuseCreds struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

func main() {
	paramsPath := "/fuse-fd/params.json"
	socketPath := "/fuse-fd/csi.sock"

	// 1. params.json が現れるまで待機（最大60秒）
	var params FuseParams
	for i := 0; i < 60; i++ {
		data, err := os.ReadFile(paramsPath)
		if err == nil {
			if err := json.Unmarshal(data, &params); err == nil && params.Bucket != "" {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if params.Bucket == "" {
		fmt.Fprintln(os.Stderr, "params.json 読み込みタイムアウト")
		os.Exit(1)
	}
	fmt.Printf("params 読み込み完了: bucket=%s endpoint=%s region=%s\n",
		params.Bucket, params.Endpoint, params.Region)

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
	if creds.AccessKey == "" || creds.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "access_key または secret_key が空です")
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

	// fusefd を子プロセス（s3fs → fusermount3-stub）に継承させるため CLOEXEC を外す
	if err := unix.SetNonblock(fusefd, false); err == nil {
		unix.FcntlInt(uintptr(fusefd), unix.F_SETFD, 0) // clear FD_CLOEXEC
	}

	// 4. passwd-s3fs を一時ファイルに書き出す
	passwdFile, err := os.CreateTemp("", "s3fs-passwd-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "passwd-s3fs ファイル作成失敗: %v\n", err)
		os.Exit(1)
	}
	passwdFile.WriteString(creds.AccessKey + ":" + creds.SecretKey)
	passwdFile.Chmod(0600)
	passwdFile.Close()
	defer os.Remove(passwdFile.Name())

	region := params.Region
	if region == "" {
		region = "us-east-1"
	}

	if err := os.MkdirAll("/mnt/fuse", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "/mnt/fuse 作成失敗: %v\n", err)
		os.Exit(1)
	}

	// 5. /dev/fuse は emptyDir subPath でコンテナ起動時に bind mount される。
	// container runtime が /dev を fresh tmpfs に差し替えるため、
	// ここでの os.Create("/dev/fuse") は不要（Dockerfile での作成も無効）。
	// libfuse3 が stat("/dev/fuse") を通過できるよう、Pod マニフェスト側で
	// emptyDir subPath を /dev/fuse にマウントすること。

	// 6. s3fs 起動
	// FUSE_PREOPEN_FD: fusermount3-stub が fusefd を libfuse に返すために使う
	// fusefd は exec を経由して fusermount3-stub に継承される（CLOEXEC なし）
	args := []string{
		params.Bucket,
		"/mnt/fuse",
		"-o", "allow_other",
		"-o", "umask=000",
		"-o", "passwd_file=" + passwdFile.Name(),
		"-o", "url=" + params.Endpoint,
		"-o", "endpoint=" + region,
		"-o", "use_path_request_style",
		"-f",
	}
	if params.NoCheckCert == "true" {
		args = append(args, "-o", "no_check_certificate")
	}

	fmt.Printf("s3fs 起動: bucket=%s endpoint=%s fusefd=%d\n", params.Bucket, params.Endpoint, fusefd)
	cmd := exec.Command("s3fs", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), fmt.Sprintf("FUSE_PREOPEN_FD=%d", fusefd))

	// s3fs をバックグラウンドで起動し、プロセスが開始したら ready ファイルを書く。
	// readinessProbe がこのファイルを検出したとき kubelet はアプリコンテナを起動する。
	readyPath := "/fuse-fd/ready"
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "s3fs 起動失敗: %v\n", err)
		os.Exit(1)
	}
	os.WriteFile(readyPath, []byte("ok\n"), 0644)
	fmt.Println("s3fs プロセス起動完了 → /fuse-fd/ready 書き込み済み")

	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "s3fs 終了: %v\n", err)
		os.Remove(readyPath)
		os.Exit(1)
	}
	os.Remove(readyPath)
}
