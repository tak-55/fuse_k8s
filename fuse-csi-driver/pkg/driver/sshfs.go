package driver

import (
	"fmt"
	"time"

	"k8s.io/klog/v2"
)

// MountSshfs performs open(/dev/fuse) + mount() at targetPath,
// then starts a background UDS server that sends SSH credentials + fusefd to the sidecar.
//
// params:
//
//	host       = SSH サーバーホスト名/IP
//	user       = SSH ユーザー名
//	remotePath = リモートパス
//	port       = SSH ポート（デフォルト: 22）
//
// secrets:
//
//	private_key = SSH 秘密鍵の内容（PEM 形式）
func (d *Driver) MountSshfs(targetPath, emptyDir string, params map[string]string, secrets map[string]string) error {
	host := params["host"]
	user := params["user"]
	remotePath := params["remotePath"]
	port := params["port"]
	if port == "" {
		port = "22"
	}

	// 必須パラメータのバリデーション
	if host == "" {
		return fmt.Errorf("volumeAttributes.host が未設定です")
	}
	if user == "" {
		return fmt.Errorf("volumeAttributes.user が未設定です")
	}
	if remotePath == "" {
		return fmt.Errorf("volumeAttributes.remotePath が未設定です")
	}
	if secrets["private_key"] == "" {
		return fmt.Errorf("nodePublishSecretRef の private_key が空です")
	}

	// Open /dev/fuse and mount at targetPath
	fusefd, err := openAndMountFuse(targetPath)
	if err != nil {
		return err
	}

	fp := FuseParams{
		Type:       "sshfs",
		Host:       host,
		User:       user,
		RemotePath: remotePath,
		Port:       port,
	}
	fc := FuseCreds{PrivateKey: secrets["private_key"]}

	stopFn, err := startFdServer(emptyDir, fp, fc, fusefd)
	if err != nil {
		closeFuse(fusefd, targetPath)
		return fmt.Errorf("UDS server 起動失敗: %w", err)
	}

	klog.Infof("sshfs fd-passing 設定完了: targetPath=%s emptyDir=%s", targetPath, emptyDir)

	d.mu.Lock()
	d.mounts[targetPath] = &mountInfo{
		fusefd:    fusefd,
		stopFdSrv: stopFn,
		fsType:    "sshfs",
		mountedAt: time.Now(),
	}
	d.mu.Unlock()
	return nil
}
