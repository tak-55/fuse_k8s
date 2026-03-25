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

	// 5. sshfs 起動
	// fusefd を ExtraFiles[0] として渡す → 子プロセス内では fd=3 (stdin=0, stdout=1, stderr=2 の次)
	const childFd = 3
	fuseFile := os.NewFile(uintptr(fusefd), "fuse")

	args := []string{
		params.User + "@" + params.Host + ":" + params.RemotePath,
		"/mnt/fuse",
		"-p", port,
		"-o", fmt.Sprintf("fd=%d", childFd),
		"-o", "rootmode=40000",
		"-o", "user_id=0",
		"-o", "group_id=0",
		"-o", "allow_other",
		"-o", "umask=000",
		"-o", "IdentityFile=" + keyFile.Name(),
		"-o", "StrictHostKeyChecking=accept-new",
		"-f",
	}

	fmt.Printf("sshfs 起動: %v\n", args)
	cmd := exec.Command("sshfs", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{fuseFile} // fd 3 in child

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "sshfs 終了: %v\n", err)
		os.Exit(1)
	}
}
