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
