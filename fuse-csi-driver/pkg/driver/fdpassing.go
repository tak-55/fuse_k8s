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

// FuseCreds は UDS 経由でサイドカーに送信する（認証情報）
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

	// mount options: fd=N で open済みの fd を指定、allow_other + umask=000 で全UID から読み書き可
	mountOpts := fmt.Sprintf("fd=%d,rootmode=40000,user_id=0,group_id=0,allow_other,umask=000", fusefd)
	if err := unix.Mount("/dev/fuse", targetPath, "fuse", unix.MS_NODEV|unix.MS_NOSUID, mountOpts); err != nil {
		unix.Close(fusefd)
		return 0, fmt.Errorf("mount 失敗 (targetPath=%s): %w", targetPath, err)
	}

	klog.Infof("FUSE mount 完了: targetPath=%s fd=%d", targetPath, fusefd)
	return fusefd, nil
}

// startFdServer writes params.json to emptyDir and starts a background UDS server
// that sends credentials + fusefd to the sidecar when it connects.
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

	socketPath := filepath.Join(emptyDir, fuseSocketName)
	os.Remove(socketPath)
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("UDS サーバー起動失敗 (%s): %w", socketPath, err)
	}
	// 0666: CSI DaemonSet (root) が作成するが、サイドカー (UID 1000) が接続する必要がある。
	// このソケットは Pod 固有の emptyDir 内にあり、同一 Pod のコンテナのみがアクセス可能。
	// 他の Pod はこのパスを hostPath マウントしない限りアクセスできないため、リスクは低い。
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
				// タイムアウトエラーの場合はループ継続（サイドカー起動待ち）
				if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
					continue
				}
				klog.Warningf("UDS accept エラー (socket=%s): %v", socketPath, err)
				return
			}
			go sendFdToSidecar(conn.(*net.UnixConn), creds, fusefd)
		}
	}()

	stopFn := func() {
		close(stopCh)
		l.Close()
		os.Remove(socketPath)
		os.Remove(filepath.Join(emptyDir, fuseParamsName))
	}
	return stopFn, nil
}

// closeFuse closes the fusefd and unmounts targetPath (used on error paths).
func closeFuse(fusefd int, targetPath string) {
	unix.Close(fusefd)
	unix.Unmount(targetPath, unix.MNT_DETACH)
}

// sendFdToSidecar sends credentials JSON and fusefd (via SCM_RIGHTS) to the sidecar.
func sendFdToSidecar(conn *net.UnixConn, creds FuseCreds, fusefd int) {
	defer conn.Close()
	credJSON, err := json.Marshal(creds)
	if err != nil {
		klog.Errorf("creds JSON marshal 失敗: %v", err)
		return
	}
	rights := unix.UnixRights(fusefd)
	if _, _, err := conn.WriteMsgUnix(credJSON, rights, nil); err != nil {
		klog.Errorf("fd 送信失敗 (fd=%d): %v", fusefd, err)
		return
	}
	klog.Infof("fusefd=%d をサイドカーに送信完了", fusefd)
}
