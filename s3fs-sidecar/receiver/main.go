package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
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
	credsPath  := "/fuse-fd/creds.json"

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

	// 2. creds.json が現れるまで待機（最大60秒）
	var creds FuseCreds
	for i := 0; i < 60; i++ {
		data, err := os.ReadFile(credsPath)
		if err == nil {
			if err := json.Unmarshal(data, &creds); err == nil && creds.AccessKey != "" {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if creds.AccessKey == "" || creds.SecretKey == "" {
		fmt.Fprintln(os.Stderr, "creds.json 読み込みタイムアウト")
		os.Exit(1)
	}
	fmt.Println("creds.json 読み込み完了")

	// 3. passwd-s3fs を一時ファイルに書き出す
	passwdFile, err := os.CreateTemp("", "s3fs-passwd-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "passwd-s3fs ファイル作成失敗: %v\n", err)
		os.Exit(1)
	}
	passwdName := passwdFile.Name()
	if _, err := passwdFile.WriteString(creds.AccessKey + ":" + creds.SecretKey); err != nil {
		os.Remove(passwdName)
		fmt.Fprintf(os.Stderr, "passwd-s3fs 書き込み失敗: %v\n", err)
		os.Exit(1)
	}
	passwdFile.Chmod(0600)
	passwdFile.Close()

	region := params.Region
	if region == "" {
		region = "us-east-1"
	}

	if err := os.MkdirAll("/mnt/fuse", 0755); err != nil {
		os.Remove(passwdName)
		fmt.Fprintf(os.Stderr, "/mnt/fuse 作成失敗: %v\n", err)
		os.Exit(1)
	}

	// 4. s3fs 起動
	// FUSERMOUNT3PROXY_FDPASSING_SOCKPATH はコンテナ env（deploy.yaml）から継承される。
	// s3fs が libfuse を初期化する際に fusermount3-proxy が呼ばれ、CSI UDS から fd を取得する。
	args := []string{
		params.Bucket,
		"/mnt/fuse",
		"-o", "allow_other",
		"-o", "umask=000",
		"-o", "passwd_file=" + passwdName,
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

	readyPath := "/fuse-fd/ready"
	if err := cmd.Start(); err != nil {
		os.Remove(passwdName)
		fmt.Fprintf(os.Stderr, "s3fs 起動失敗: %v\n", err)
		os.Exit(1)
	}
	os.WriteFile(readyPath, []byte("ok\n"), 0644)
	fmt.Println("s3fs プロセス起動完了 → /fuse-fd/ready 書き込み済み")

	if err := cmd.Wait(); err != nil {
		os.Remove(passwdName)
		fmt.Fprintf(os.Stderr, "s3fs 終了: %v\n", err)
		os.Remove(readyPath)
		os.Exit(1)
	}
	os.Remove(passwdName)
	os.Remove(readyPath)
}
