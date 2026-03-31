package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
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
	credsPath  := "/fuse-fd/creds.json"

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

	// 2. creds.json が現れるまで待機（最大60秒）
	var creds FuseCreds
	for i := 0; i < 60; i++ {
		data, err := os.ReadFile(credsPath)
		if err == nil {
			if err := json.Unmarshal(data, &creds); err == nil && creds.PrivateKey != "" {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if creds.PrivateKey == "" {
		fmt.Fprintln(os.Stderr, "creds.json 読み込みタイムアウト")
		os.Exit(1)
	}
	fmt.Println("creds.json 読み込み完了")

	// 3. SSH 秘密鍵を一時ファイルに書き出す
	keyFile, err := os.CreateTemp("", "sshfs-key-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "鍵ファイル作成失敗: %v\n", err)
		os.Exit(1)
	}
	keyName := keyFile.Name()
	if _, err := keyFile.WriteString(creds.PrivateKey); err != nil {
		os.Remove(keyName)
		fmt.Fprintf(os.Stderr, "鍵ファイル書き込み失敗: %v\n", err)
		os.Exit(1)
	}
	if err := keyFile.Chmod(0600); err != nil {
		keyFile.Close()
		os.Remove(keyName)
		fmt.Fprintf(os.Stderr, "鍵ファイル chmod 失敗: %v\n", err)
		os.Exit(1)
	}
	keyFile.Close()

	port := params.Port
	if port == "" {
		port = "22"
	}

	if err := os.MkdirAll("/mnt/fuse", 0755); err != nil {
		os.Remove(keyName)
		fmt.Fprintf(os.Stderr, "/mnt/fuse 作成失敗: %v\n", err)
		os.Exit(1)
	}

	// 4. sshfs 起動
	// FUSERMOUNT3PROXY_FDPASSING_SOCKPATH はコンテナ env（deploy.yaml）から継承される。
	// sshfs が libfuse を初期化する際に fusermount3-proxy が呼ばれ、CSI UDS から fd を取得する。
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

	readyPath := "/fuse-fd/ready"
	if err := cmd.Start(); err != nil {
		os.Remove(keyName)
		fmt.Fprintf(os.Stderr, "sshfs 起動失敗: %v\n", err)
		os.Exit(1)
	}
	os.WriteFile(readyPath, []byte("ok\n"), 0644)
	fmt.Println("sshfs プロセス起動完了 → /fuse-fd/ready 書き込み済み")

	if err := cmd.Wait(); err != nil {
		os.Remove(keyName)
		fmt.Fprintf(os.Stderr, "sshfs 終了: %v\n", err)
		os.Remove(readyPath)
		os.Exit(1)
	}
	os.Remove(keyName)
	os.Remove(readyPath)
}
