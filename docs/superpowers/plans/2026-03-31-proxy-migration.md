# fusermount3-proxy 移行 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** fusermount3-stub（push モデル）を fusermount3-proxy（pull モデル）に置き換え、meta-fuse-csi-plugin と同じアーキテクチャで sshfs・s3fs を動作させる。

**Architecture:** CSI ドライバーは fd を UDS で送信するが、接続してくるのは sidecar receiver ではなく fusermount3-proxy（libfuse が呼ぶ）に変わる。receiver は UDS 接続を行わず、creds.json を読んでそのまま sshfs/s3fs を exec する。

**Tech Stack:** Go 1.21, golang.org/x/sys/unix, Kubernetes CSI, FUSE3

---

## ファイルマップ

| アクション | ファイル | 内容 |
|--|--|--|
| 削除 | `sshfs/`, `s3fs/`, `csi/fuse-csi-driver-daemonset.yaml`, `.devcontainer/`, `docs/guide-kind-setup-and-test.md`, `tests/reports/kind-capsule-kyverno-report.txt` | kind 関連 |
| 変更 | `fuse-csi-driver/pkg/driver/fdpassing.go` | creds.json 書き出し、UDS は fd のみ送信 |
| 作成→削除 | `sshfs-sidecar/fusermount3-proxy/main.go` / `sshfs-sidecar/fusermount3-stub/` | proxy に置き換え |
| 作成→削除 | `s3fs-sidecar/fusermount3-proxy/main.go` / `s3fs-sidecar/fusermount3-stub/` | proxy に置き換え |
| 変更 | `sshfs-sidecar/receiver/main.go` | UDS 削除、creds.json 読み込み |
| 変更 | `s3fs-sidecar/receiver/main.go` | 同上 |
| 変更 | `sshfs-sidecar/Dockerfile` | fusermount3-proxy ビルドに変更 |
| 変更 | `s3fs-sidecar/Dockerfile` | 同上 |
| 作成 | `examples/sshfs/deploy.yaml` | FUSERMOUNT3PROXY_FDPASSING_SOCKPATH 追加 |
| 作成 | `examples/s3fs/deploy.yaml` | 同上 |

---

## Task 1: kind 関連ファイルの削除

**Files:**
- Delete: `sshfs/deploy-kind.yaml`
- Delete: `s3fs/deploy-kind.yaml`
- Delete: `csi/fuse-csi-driver-daemonset.yaml`
- Delete: `.devcontainer/` (全体)
- Delete: `docs/guide-kind-setup-and-test.md`
- Delete: `tests/reports/kind-capsule-kyverno-report.txt`
- Delete: `overlays/local/kind/` (存在する場合)

- [ ] **Step 1: kind ファイルを削除する**

```bash
rm -f sshfs/deploy-kind.yaml
rm -f s3fs/deploy-kind.yaml
rm -f csi/fuse-csi-driver-daemonset.yaml
rm -rf .devcontainer/
rm -f docs/guide-kind-setup-and-test.md
rm -rf tests/reports/
rm -rf overlays/local/kind/
```

- [ ] **Step 2: 削除を確認する**

```bash
git status
```

期待結果: 削除されたファイル一覧が表示される。`sshfs/deploy.yaml` や `csi/fuse-csi-driver.yaml` が残っていることを確認する。

- [ ] **Step 3: コミット**

```bash
git add -A
git commit -m "kind関連ファイルを削除"
```

---

## Task 2: fdpassing.go の変更（creds.json 書き出し、UDS fd のみ送信）

**Files:**
- Modify: `fuse-csi-driver/pkg/driver/fdpassing.go`

**変更内容:**
1. `fuseCredsName = "creds.json"` 定数を追加
2. `startFdServer` 内で creds.json を emptyDir に書き出す（0600、chown 1000:1000）
3. `sendFdToSidecar` から credentials JSON 送信を削除（fd のみ送信）
4. `stopFn` で creds.json も削除する
5. `node.go` のコメントを更新

- [ ] **Step 1: 既存テストが通ることを確認**

```bash
cd fuse-csi-driver
go test ./pkg/driver/... -v
```

期待結果: `PASS` が出る（テストはバリデーションのみなので /dev/fuse 不要）

- [ ] **Step 2: fdpassing.go を書き換える**

`fuse-csi-driver/pkg/driver/fdpassing.go` の内容を以下に置き換える:

```go
//go:build linux

package driver

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

const (
	fuseEmptyDirName = "fuse-fd"  // User Pod の emptyDir volume 名（規約）
	fuseSocketName   = "csi.sock"
	fuseParamsName   = "params.json"
	fuseCredsName    = "creds.json"
	kubeletPodsDir   = "/var/lib/kubelet/pods"
)

// FuseParams は emptyDir の params.json に書き出す（認証情報なし）
type FuseParams struct {
	Type        string `json:"type"`
	Host        string `json:"host,omitempty"`
	User        string `json:"user,omitempty"`
	RemotePath  string `json:"remotePath,omitempty"`
	Port        string `json:"port,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	NoCheckCert string `json:"noCheckCert,omitempty"`
}

// FuseCreds は emptyDir の creds.json に書き出す（認証情報）
type FuseCreds struct {
	PrivateKey string `json:"private_key,omitempty"`
	AccessKey  string `json:"access_key,omitempty"`
	SecretKey  string `json:"secret_key,omitempty"`
}

// emptyDirHostPath returns the host filesystem path of the fuse-fd emptyDir volume for a given pod UID.
// CSI DaemonSet は /var/lib/kubelet を hostPath でマウント済みのため直接アクセス可能。
func emptyDirHostPath(podUID string) string {
	return filepath.Join(kubeletPodsDir, podUID, "volumes", "kubernetes.io~empty-dir", fuseEmptyDirName)
}

// openAndMountFuse opens /dev/fuse and mounts it at targetPath.
// Returns the open fd (must be kept open while FUSE filesystem is active).
func openAndMountFuse(targetPath string) (int, error) {
	fusefd, err := unix.Open("/dev/fuse", unix.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("open /dev/fuse 失敗: %w", err)
	}

	mountOpts := fmt.Sprintf("fd=%d,rootmode=40000,user_id=0,group_id=0,allow_other,default_permissions", fusefd)
	klog.Infof("FUSE mount 試行: targetPath=%s fd=%d", targetPath, fusefd)
	if err := unix.Mount("/dev/fuse", targetPath, "fuse", 0, mountOpts); err != nil {
		unix.Close(fusefd)
		return 0, fmt.Errorf("mount 失敗 (targetPath=%s): %w", targetPath, err)
	}

	klog.Infof("FUSE mount 完了: targetPath=%s fd=%d", targetPath, fusefd)
	return fusefd, nil
}

// startFdServer writes params.json and creds.json to emptyDir, then starts a background UDS server
// that sends fusefd to fusermount3-proxy when it connects.
// Returns stopFn to shut down the server (call from NodeUnpublishVolume).
func startFdServer(emptyDir string, params FuseParams, creds FuseCreds, fusefd int) (func(), error) {
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		return nil, fmt.Errorf("emptyDir 作成失敗: %w", err)
	}

	// Write params.json (no credentials — safe to write to emptyDir)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(emptyDir, fuseParamsName), paramsJSON, 0644); err != nil {
		return nil, fmt.Errorf("params.json 書き込み失敗: %w", err)
	}

	// Write creds.json (credentials — restricted permissions, chown to UID 1000)
	credsJSON, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}
	credsPath := filepath.Join(emptyDir, fuseCredsName)
	if err := os.WriteFile(credsPath, credsJSON, 0600); err != nil {
		return nil, fmt.Errorf("creds.json 書き込み失敗: %w", err)
	}
	if err := os.Chown(credsPath, 1000, 1000); err != nil {
		klog.Warningf("creds.json chown 失敗（UID 1000 で読めない可能性）: %v", err)
	}

	socketPath := filepath.Join(emptyDir, fuseSocketName)
	os.Remove(socketPath)
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("UDS サーバー起動失敗 (%s): %w", socketPath, err)
	}
	// 0666: CSI DaemonSet (root) が作成するが、サイドカー (UID 1000) 内の fusermount3-proxy が接続する。
	// このソケットは Pod 固有の emptyDir 内にあり、同一 Pod のコンテナのみがアクセス可能。
	if err := os.Chmod(socketPath, 0666); err != nil {
		l.Close()
		return nil, err
	}

	stopCh := make(chan struct{})
	go func() {
		defer l.Close()
		for {
			l.(*net.UnixListener).SetDeadline(time.Now().Add(30 * time.Second))
			conn, err := l.Accept()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
				}
				if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
					continue
				}
				klog.Warningf("UDS accept エラー (socket=%s): %v", socketPath, err)
				return
			}
			go sendFdToProxy(conn.(*net.UnixConn), fusefd)
		}
	}()

	stopFn := func() {
		close(stopCh)
		l.Close()
		os.Remove(socketPath)
		os.Remove(filepath.Join(emptyDir, fuseParamsName))
		os.Remove(filepath.Join(emptyDir, fuseCredsName))
	}
	return stopFn, nil
}

// closeFuse closes the fusefd and unmounts targetPath (used on error paths).
func closeFuse(fusefd int, targetPath string) {
	unix.Close(fusefd)
	unix.Unmount(targetPath, unix.MNT_DETACH)
}

// sendFdToProxy sends fusefd (via SCM_RIGHTS) to fusermount3-proxy.
func sendFdToProxy(conn *net.UnixConn, fusefd int) {
	defer conn.Close()
	rights := unix.UnixRights(fusefd)
	if _, _, err := conn.WriteMsgUnix([]byte{0}, rights, nil); err != nil {
		klog.Errorf("fd 送信失敗 (fd=%d): %v", fusefd, err)
		return
	}
	klog.Infof("fusefd=%d を fusermount3-proxy に送信完了", fusefd)
}
```

- [ ] **Step 3: node.go のコメントを更新する**

`fuse-csi-driver/pkg/driver/node.go` の 85 行目のコメントを変更:

```
// 変更前:
info.stopFdSrv()               // UDS server 停止 + params.json / csi.sock 削除

// 変更後:
info.stopFdSrv()               // UDS server 停止 + params.json / creds.json / csi.sock 削除
```

- [ ] **Step 4: ビルドと既存テストを実行**

```bash
cd fuse-csi-driver
go build ./...
go test ./pkg/driver/... -v
```

期待結果: ビルド成功、全テスト PASS

- [ ] **Step 5: コミット**

```bash
git add fuse-csi-driver/pkg/driver/fdpassing.go fuse-csi-driver/pkg/driver/node.go
git commit -m "fdpassing: creds.jsonをemptyDirに書き出し、UDSはfdのみ送信に変更"
```

---

## Task 3: sshfs-sidecar fusermount3-proxy の実装

**Files:**
- Create: `sshfs-sidecar/fusermount3-proxy/main.go`
- Delete: `sshfs-sidecar/fusermount3-stub/main.go`（ディレクトリごと）

- [ ] **Step 1: fusermount3-proxy ディレクトリを作成し main.go を書く**

```bash
mkdir -p sshfs-sidecar/fusermount3-proxy
```

`sshfs-sidecar/fusermount3-proxy/main.go` を以下の内容で作成:

```go
// fusermount3-proxy: libfuse3 から呼ばれる fusermount3 の代替バイナリ。
// CSI DaemonSet の UDS ソケットに接続して fusefd を受け取り、
// _FUSE_COMMFD ソケット経由で libfuse に返す。
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func main() {
	// アンマウント呼び出し（_FUSE_COMMFD なし）→ 何もせず正常終了
	commFDStr := os.Getenv("_FUSE_COMMFD")
	if commFDStr == "" {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: アンマウント呼び出し → 正常終了")
		os.Exit(0)
	}

	socketPath := os.Getenv("FUSERMOUNT3PROXY_FDPASSING_SOCKPATH")
	if socketPath == "" {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: FUSERMOUNT3PROXY_FDPASSING_SOCKPATH が未設定")
		os.Exit(1)
	}

	commFD, err := strconv.Atoi(commFDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: _FUSE_COMMFD parse 失敗: %v\n", err)
		os.Exit(1)
	}

	// CSI UDS ソケットに接続して fusefd を SCM_RIGHTS で受信
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: CSI UDS 接続失敗 (%s): %v\n", socketPath, err)
		os.Exit(1)
	}
	defer conn.Close()

	unixConn := conn.(*net.UnixConn)
	buf := make([]byte, 500)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unixConn.ReadMsgUnix(buf, oob)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: fd 受信失敗: %v\n", err)
		os.Exit(1)
	}

	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) == 0 {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: SCM_RIGHTS parse 失敗")
		os.Exit(1)
	}
	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil || len(fds) == 0 {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: UnixRights parse 失敗")
		os.Exit(1)
	}
	fuseFd := fds[0]
	fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd=%d を CSI から受信\n", fuseFd)

	// libfuse の _FUSE_COMMFD ソケットに fusefd を SCM_RIGHTS で返す
	commFdFile := os.NewFile(uintptr(commFD), "commfd")
	commConn, err := net.FileConn(commFdFile)
	commFdFile.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: commfd FileConn 失敗: %v\n", err)
		os.Exit(1)
	}
	defer commConn.Close()

	commUnix := commConn.(*net.UnixConn)
	rights := unix.UnixRights(fuseFd)
	if _, _, err := commUnix.WriteMsgUnix([]byte{0}, rights, nil); err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd 送信失敗: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd=%d を libfuse に送信完了\n", fuseFd)
}
```

- [ ] **Step 2: ビルド確認**

```bash
cd sshfs-sidecar
CGO_ENABLED=0 GOOS=linux go build -o /tmp/fusermount3-proxy ./fusermount3-proxy/
echo "ビルド成功: $?"
```

期待結果: exit code 0

- [ ] **Step 3: fusermount3-stub を削除**

```bash
rm -rf sshfs-sidecar/fusermount3-stub/
```

- [ ] **Step 4: コミット**

```bash
git add sshfs-sidecar/fusermount3-proxy/ sshfs-sidecar/fusermount3-stub/
git commit -m "sshfs-sidecar: fusermount3-stubをfusermount3-proxyに置き換え"
```

---

## Task 4: s3fs-sidecar fusermount3-proxy の実装

**Files:**
- Create: `s3fs-sidecar/fusermount3-proxy/main.go`
- Delete: `s3fs-sidecar/fusermount3-stub/main.go`（ディレクトリごと）

- [ ] **Step 1: fusermount3-proxy ディレクトリを作成し main.go を書く**

```bash
mkdir -p s3fs-sidecar/fusermount3-proxy
```

`s3fs-sidecar/fusermount3-proxy/main.go` を以下の内容で作成（sshfs と同一コード）:

```go
// fusermount3-proxy: libfuse3 から呼ばれる fusermount3 の代替バイナリ。
// CSI DaemonSet の UDS ソケットに接続して fusefd を受け取り、
// _FUSE_COMMFD ソケット経由で libfuse に返す。
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func main() {
	// アンマウント呼び出し（_FUSE_COMMFD なし）→ 何もせず正常終了
	commFDStr := os.Getenv("_FUSE_COMMFD")
	if commFDStr == "" {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: アンマウント呼び出し → 正常終了")
		os.Exit(0)
	}

	socketPath := os.Getenv("FUSERMOUNT3PROXY_FDPASSING_SOCKPATH")
	if socketPath == "" {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: FUSERMOUNT3PROXY_FDPASSING_SOCKPATH が未設定")
		os.Exit(1)
	}

	commFD, err := strconv.Atoi(commFDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: _FUSE_COMMFD parse 失敗: %v\n", err)
		os.Exit(1)
	}

	// CSI UDS ソケットに接続して fusefd を SCM_RIGHTS で受信
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: CSI UDS 接続失敗 (%s): %v\n", socketPath, err)
		os.Exit(1)
	}
	defer conn.Close()

	unixConn := conn.(*net.UnixConn)
	buf := make([]byte, 500)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unixConn.ReadMsgUnix(buf, oob)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: fd 受信失敗: %v\n", err)
		os.Exit(1)
	}

	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) == 0 {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: SCM_RIGHTS parse 失敗")
		os.Exit(1)
	}
	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil || len(fds) == 0 {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: UnixRights parse 失敗")
		os.Exit(1)
	}
	fuseFd := fds[0]
	fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd=%d を CSI から受信\n", fuseFd)

	// libfuse の _FUSE_COMMFD ソケットに fusefd を SCM_RIGHTS で返す
	commFdFile := os.NewFile(uintptr(commFD), "commfd")
	commConn, err := net.FileConn(commFdFile)
	commFdFile.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: commfd FileConn 失敗: %v\n", err)
		os.Exit(1)
	}
	defer commConn.Close()

	commUnix := commConn.(*net.UnixConn)
	rights := unix.UnixRights(fuseFd)
	if _, _, err := commUnix.WriteMsgUnix([]byte{0}, rights, nil); err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd 送信失敗: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd=%d を libfuse に送信完了\n", fuseFd)
}
```

- [ ] **Step 2: ビルド確認**

```bash
cd s3fs-sidecar
CGO_ENABLED=0 GOOS=linux go build -o /tmp/fusermount3-proxy-s3fs ./fusermount3-proxy/
echo "ビルド成功: $?"
```

期待結果: exit code 0

- [ ] **Step 3: fusermount3-stub を削除**

```bash
rm -rf s3fs-sidecar/fusermount3-stub/
```

- [ ] **Step 4: コミット**

```bash
git add s3fs-sidecar/fusermount3-proxy/ s3fs-sidecar/fusermount3-stub/
git commit -m "s3fs-sidecar: fusermount3-stubをfusermount3-proxyに置き換え"
```

---

## Task 5: sshfs-sidecar receiver の変更

**Files:**
- Modify: `sshfs-sidecar/receiver/main.go`

**変更内容:** UDS 接続・fd 受信ロジックを削除し、creds.json 読み込みに変更。`net` と `golang.org/x/sys/unix` の import も削除。

- [ ] **Step 1: receiver/main.go を書き換える**

`sshfs-sidecar/receiver/main.go` の内容を以下に置き換える:

```go
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
		fmt.Fprintf(os.Stderr, "sshfs 起動失敗: %v\n", err)
		os.Exit(1)
	}
	os.WriteFile(readyPath, []byte("ok\n"), 0644)
	fmt.Println("sshfs プロセス起動完了 → /fuse-fd/ready 書き込み済み")

	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "sshfs 終了: %v\n", err)
		os.Remove(readyPath)
		os.Exit(1)
	}
	os.Remove(readyPath)
}
```

- [ ] **Step 2: ビルド確認**

```bash
cd sshfs-sidecar
CGO_ENABLED=0 GOOS=linux go build -o /tmp/receiver-sshfs ./receiver/
echo "ビルド成功: $?"
```

期待結果: exit code 0

- [ ] **Step 3: コミット**

```bash
git add sshfs-sidecar/receiver/main.go
git commit -m "sshfs-sidecar receiver: UDS削除、creds.json読み込みに変更"
```

---

## Task 6: s3fs-sidecar receiver の変更

**Files:**
- Modify: `s3fs-sidecar/receiver/main.go`

- [ ] **Step 1: receiver/main.go を書き換える**

`s3fs-sidecar/receiver/main.go` の内容を以下に置き換える:

```go
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

	// 4. s3fs 起動
	// FUSERMOUNT3PROXY_FDPASSING_SOCKPATH はコンテナ env（deploy.yaml）から継承される。
	// s3fs が libfuse を初期化する際に fusermount3-proxy が呼ばれ、CSI UDS から fd を取得する。
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

	fmt.Printf("s3fs 起動: bucket=%s endpoint=%s\n", params.Bucket, params.Endpoint)
	cmd := exec.Command("s3fs", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

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
```

- [ ] **Step 2: ビルド確認**

```bash
cd s3fs-sidecar
CGO_ENABLED=0 GOOS=linux go build -o /tmp/receiver-s3fs ./receiver/
echo "ビルド成功: $?"
```

期待結果: exit code 0

- [ ] **Step 3: コミット**

```bash
git add s3fs-sidecar/receiver/main.go
git commit -m "s3fs-sidecar receiver: UDS削除、creds.json読み込みに変更"
```

---

## Task 7: sshfs-sidecar Dockerfile の変更

**Files:**
- Modify: `sshfs-sidecar/Dockerfile`

- [ ] **Step 1: Dockerfile を書き換える**

`sshfs-sidecar/Dockerfile` の内容を以下に置き換える:

```dockerfile
FROM golang:1.21 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /receiver ./receiver/
RUN CGO_ENABLED=0 GOOS=linux go build -o /fusermount3-proxy ./fusermount3-proxy/

FROM ubuntu:22.04
RUN apt-get update && apt-get install -y \
    sshfs \
    fuse3 \
    && rm -rf /var/lib/apt/lists/*
RUN echo "user_allow_other" >> /etc/fuse.conf
RUN mkdir -p /mnt/fuse && chmod 777 /mnt/fuse
# OpenSSH クライアントは getpwuid(geteuid()) でユーザーを検索するため
# uid 1000 のエントリが /etc/passwd にないと "No user exists for uid 1000" で失敗する
RUN useradd -u 1000 -m -s /bin/bash appuser

COPY --from=builder /receiver /bin/receiver
# 本物の fusermount3 を proxy に置き換える。
# proxy は CSI UDS に接続して fusefd を取得し、libfuse に返す。
COPY --from=builder /fusermount3-proxy /usr/bin/fusermount3
RUN ln -sf /usr/bin/fusermount3 /usr/bin/fusermount
RUN chmod 4755 /usr/bin/fusermount3
RUN chmod 4755 /usr/bin/fusermount
ENTRYPOINT ["/bin/receiver"]
```

- [ ] **Step 2: コミット**

```bash
git add sshfs-sidecar/Dockerfile
git commit -m "sshfs-sidecar Dockerfile: fusermount3-proxyビルドに変更"
```

---

## Task 8: s3fs-sidecar Dockerfile の変更

**Files:**
- Modify: `s3fs-sidecar/Dockerfile`

- [ ] **Step 1: Dockerfile を書き換える**

`s3fs-sidecar/Dockerfile` の内容を以下に置き換える:

```dockerfile
FROM golang:1.21 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /receiver ./receiver/
RUN CGO_ENABLED=0 GOOS=linux go build -o /fusermount3-proxy ./fusermount3-proxy/

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
# 本物の fusermount3 を proxy に置き換える。
# proxy は CSI UDS に接続して fusefd を取得し、libfuse に返す。
COPY --from=builder /fusermount3-proxy /usr/bin/fusermount3
RUN ln -sf /usr/bin/fusermount3 /usr/bin/fusermount
RUN chmod 4755 /usr/bin/fusermount3
RUN chmod 4755 /usr/bin/fusermount
ENTRYPOINT ["/bin/receiver"]
```

- [ ] **Step 2: コミット**

```bash
git add s3fs-sidecar/Dockerfile
git commit -m "s3fs-sidecar Dockerfile: fusermount3-proxyビルドに変更"
```

---

## Task 9: examples/ マニフェストの作成

**Files:**
- Create: `examples/sshfs/deploy.yaml`
- Create: `examples/s3fs/deploy.yaml`
- Delete: `sshfs/`（ディレクトリごと）
- Delete: `s3fs/`（ディレクトリごと）

- [ ] **Step 1: examples ディレクトリを作成する**

```bash
mkdir -p examples/sshfs examples/s3fs
```

- [ ] **Step 2: examples/sshfs/deploy.yaml を作成する**

```yaml
# =============================================================================
# fusermount3-proxy アーキテクチャ用 Pod サンプル（本番環境）
# sshfs-sidecar が FUSE データプレーンを担当する
#
# 使い方:
#   1. SSH 秘密鍵 Secret を作成（--from-file を使うこと）
#      kubectl create secret generic ssh-key \
#        --from-file=private_key=~/.ssh/id_ed25519 \
#        -n <tenant-namespace>
#
#   2. volumeAttributes の host / user / remotePath を実環境に合わせて変更
#
#   3. Pod をデプロイ
#      kubectl apply -f examples/sshfs/deploy.yaml -n <tenant-namespace>
#
# 注意:
#   - FUSE CSI ボリュームを持つ Pod は hostUsers: false が Kyverno から自動除外される
#     （/dev/fuse FUSE は MOUNT_ATTR_IDMAP 非対応のため）
#   - securityContext（PSS restricted）は Kyverno が自動注入
#   - 秘密鍵は --from-literal ではなく --from-file で作成すること
#     （--from-literal はシェル展開で末尾改行を除去するため error in libcrypto が発生する）
#   - SSH サーバー側は SFTP + ChrootDirectory 構成を推奨（docs/design-sftp-chroot-ssh-server.md）
# =============================================================================
apiVersion: v1
kind: Pod
metadata:
  name: sshfs-example
  # namespace は Capsule テナント namespace に変更すること
spec:
  initContainers:
    # /dev/fuse 用の空ファイルを作成（libfuse が stat("/dev/fuse") を要求するため）
    - name: create-fuse-device
      image: busybox:stable
      command: ["touch", "/fuse-dev/fuse"]
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
      volumeMounts:
        - name: fuse-device
          mountPath: /fuse-dev

    # sshfs-sidecar: creds.json を読んで sshfs を起動するサイドカー
    # restartPolicy: Always → sidecar initContainer（app より先に起動、app と並行して稼働）
    - name: sshfs-sidecar
      image: ghcr.io/tak-55/sshfs-sidecar:latest
      imagePullPolicy: Always
      restartPolicy: Always
      env:
        # fusermount3-proxy が CSI UDS に接続するためのソケットパス
        - name: FUSERMOUNT3PROXY_FDPASSING_SOCKPATH
          value: "/fuse-fd/csi.sock"
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        allowPrivilegeEscalation: true  # fusermount3-proxy の setuid (4755) 実行に必要
      readinessProbe:
        exec:
          command: ["test", "-f", "/fuse-fd/ready"]
        initialDelaySeconds: 2
        periodSeconds: 2
        failureThreshold: 30
      resources:
        requests:
          cpu: 50m
          memory: 64Mi
        limits:
          cpu: 200m
          memory: 256Mi
      volumeMounts:
        - name: fuse-fd
          mountPath: /fuse-fd
        - name: fuse-device
          mountPath: /dev/fuse
          subPath: fuse

  containers:
    - name: app
      image: busybox:stable
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
      command: ["/bin/sh", "-c"]
      args:
        - |
          echo "=== マウント確認 ==="
          ls -la /data
          echo "=== 書き込みテスト ==="
          echo "hello fd-pass $(date)" > /data/test.txt && cat /data/test.txt
          sleep infinity
      volumeMounts:
        - name: sshfs-vol
          mountPath: /data

  volumes:
    - name: sshfs-vol
      csi:
        driver: fuse.csi.fuse-k8s.io
        volumeAttributes:
          type: sshfs
          host: "your-ssh-host"
          user: "your-user"
          remotePath: "/data"
          port: "22"
        nodePublishSecretRef:
          name: ssh-key
    - name: fuse-fd
      emptyDir: {}
    - name: fuse-device
      emptyDir: {}

  terminationGracePeriodSeconds: 30
```

- [ ] **Step 3: examples/s3fs/deploy.yaml を作成する**

```yaml
# =============================================================================
# fusermount3-proxy アーキテクチャ用 Pod サンプル（本番環境）
# s3fs-sidecar が FUSE データプレーンを担当する
#
# 使い方:
#   1. S3 認証情報 Secret を作成
#      kubectl create secret generic s3-credentials \
#        --from-literal=access_key=YOUR_ACCESS_KEY \
#        --from-literal=secret_key=YOUR_SECRET_KEY \
#        -n <tenant-namespace>
#
#   2. volumeAttributes の bucket / endpoint を実環境に合わせて変更
#
#   3. Pod をデプロイ
#      kubectl apply -f examples/s3fs/deploy.yaml -n <tenant-namespace>
# =============================================================================
apiVersion: v1
kind: Pod
metadata:
  name: s3fs-example
  # namespace は Capsule テナント namespace に変更すること
spec:
  initContainers:
    - name: create-fuse-device
      image: busybox:stable
      command: ["touch", "/fuse-dev/fuse"]
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
      volumeMounts:
        - name: fuse-device
          mountPath: /fuse-dev

    - name: s3fs-sidecar
      image: ghcr.io/tak-55/s3fs-sidecar:latest
      imagePullPolicy: Always
      restartPolicy: Always
      env:
        - name: FUSERMOUNT3PROXY_FDPASSING_SOCKPATH
          value: "/fuse-fd/csi.sock"
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        allowPrivilegeEscalation: true
      readinessProbe:
        exec:
          command: ["test", "-f", "/fuse-fd/ready"]
        initialDelaySeconds: 2
        periodSeconds: 2
        failureThreshold: 30
      resources:
        requests:
          cpu: 50m
          memory: 64Mi
        limits:
          cpu: 200m
          memory: 256Mi
      volumeMounts:
        - name: fuse-fd
          mountPath: /fuse-fd
        - name: fuse-device
          mountPath: /dev/fuse
          subPath: fuse

  containers:
    - name: app
      image: busybox:stable
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
      command: ["/bin/sh", "-c"]
      args:
        - |
          echo "=== マウント確認 ==="
          ls -la /data
          echo "=== 書き込みテスト ==="
          echo "hello s3 fd-pass $(date)" > /data/test.txt && cat /data/test.txt
          sleep infinity
      volumeMounts:
        - name: s3fs-vol
          mountPath: /data

  volumes:
    - name: s3fs-vol
      csi:
        driver: fuse.csi.fuse-k8s.io
        volumeAttributes:
          type: s3fs
          bucket: "your-bucket"
          endpoint: "https://your-s3-endpoint"
          region: "us-east-1"
          noCheckCert: "false"
        nodePublishSecretRef:
          name: s3-credentials
    - name: fuse-fd
      emptyDir: {}
    - name: fuse-device
      emptyDir: {}

  terminationGracePeriodSeconds: 30
```

- [ ] **Step 4: 旧 sshfs/ と s3fs/ ディレクトリを削除する**

```bash
rm -rf sshfs/ s3fs/
```

- [ ] **Step 5: コミット**

```bash
git add examples/
git rm -r sshfs/ s3fs/
git commit -m "examples/にsshfs・s3fsマニフェストを移動、FUSERMOUNT3PROXY_FDPASSING_SOCKPATH追加"
```

---

## Task 10: CLAUDE.md の更新

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: CLAUDE.md を更新する**

CLAUDE.md の以下のセクションを更新する:

**Deploy Commands** セクション: `sshfs/deploy-kind.yaml` / `s3fs/deploy-kind.yaml` の参照を削除し、`examples/` パスに変更:

```bash
# 変更前
kubectl apply -f sshfs/deploy-kind.yaml
kubectl apply -f s3fs/deploy-kind.yaml

# 変更後
kubectl apply -f examples/sshfs/deploy.yaml -n <namespace>
kubectl apply -f examples/s3fs/deploy.yaml -n <namespace>
```

**Architecture** セクションの説明を proxy 方式に更新:

```
fusermount3-stub → intercepts libfuse mount calls, returns pre-opened fd
```
↓
```
fusermount3-proxy → connects to CSI UDS socket when called by libfuse, receives fd via SCM_RIGHTS
```

**Key Component Relationships** セクション更新:

```
**`sshfs-sidecar/fusermount3-stub/main.go`** → **`sshfs-sidecar/fusermount3-proxy/main.go`**
```

- [ ] **Step 2: コミット**

```bash
git add CLAUDE.md
git commit -m "CLAUDE.md: proxy方式に合わせてドキュメントを更新"
```

---

## 検証手順

全タスク完了後、以下で動作確認する:

```bash
# 1. CSI ドライバーのビルド確認
cd fuse-csi-driver && go build ./... && go test ./pkg/driver/... -v

# 2. sshfs-sidecar のビルド確認
cd sshfs-sidecar && go build ./...

# 3. s3fs-sidecar のビルド確認
cd s3fs-sidecar && go build ./...

# 4. Docker イメージのビルド確認（実際のクラスタへのデプロイ前に）
docker build -t fuse-csi-driver:test ./fuse-csi-driver/
docker build -t sshfs-sidecar:test ./sshfs-sidecar/
docker build -t s3fs-sidecar:test ./s3fs-sidecar/
```
