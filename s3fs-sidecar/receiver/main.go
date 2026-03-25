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

	// 5. s3fs 起動
	// fusefd を ExtraFiles[0] として渡す → 子プロセス内では fd=3 (stdin=0, stdout=1, stderr=2 の次)
	const childFd = 3
	fuseFile := os.NewFile(uintptr(fusefd), "fuse")

	args := []string{
		params.Bucket,
		"/mnt/fuse",
		"-o", fmt.Sprintf("fd=%d", childFd),
		"-o", "rootmode=40000",
		"-o", "user_id=0",
		"-o", "group_id=0",
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

	fmt.Printf("s3fs 起動: bucket=%s endpoint=%s\n", params.Bucket, params.Endpoint)
	cmd := exec.Command("s3fs", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{fuseFile} // fd 3 in child

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "s3fs 終了: %v\n", err)
		os.Exit(1)
	}
}
