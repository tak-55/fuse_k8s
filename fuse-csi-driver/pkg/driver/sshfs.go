package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// MountSshfs は sshfs を起動して targetPath にマウントする
//
// params:
//
//	host              = SSH サーバーホスト名/IP
//	user              = SSH ユーザー名
//	remotePath        = リモートパス
//	port              = SSH ポート（デフォルト: 22）
//	strictHostKeyCheck = "true"(デフォルト, accept-new) | "false"（テスト用）
//
// secrets:
//
//	private_key = SSH 秘密鍵の内容（PEM 形式）
func (d *Driver) MountSshfs(targetPath string, params map[string]string, secrets map[string]string) error {
	host := params["host"]
	user := params["user"]
	remotePath := params["remotePath"]
	port := params["port"]

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

	if port == "" {
		port = "22"
	}
	strictCheck := "accept-new"
	if params["strictHostKeyCheck"] == "false" {
		strictCheck = "no"
	}

	// 秘密鍵を一時ファイルに書き出す
	// /tmp 以下に作成し、プロセス終了またはアンマウント時に削除する
	keyFile, err := os.CreateTemp("", "fuse-csi-sshkey-*")
	if err != nil {
		return fmt.Errorf("秘密鍵一時ファイル作成失敗: %w", err)
	}
	if _, err := keyFile.WriteString(secrets["private_key"]); err != nil {
		os.Remove(keyFile.Name())
		return fmt.Errorf("秘密鍵書き込み失敗: %w", err)
	}
	if err := keyFile.Chmod(0600); err != nil {
		os.Remove(keyFile.Name())
		return err
	}
	keyFile.Close()

	knownHostsFile := filepath.Join(os.TempDir(), "known_hosts_"+sanitize(host))

	args := []string{
		user + "@" + host + ":" + remotePath,
		targetPath,
		"-p", port,
		"-o", "StrictHostKeyChecking=" + strictCheck,
		"-o", "UserKnownHostsFile=" + knownHostsFile,
		"-o", "IdentityFile=" + keyFile.Name(),
		// allow_other: CSI DaemonSet (root) がマウントしたファイルに
		// ユーザーコンテナ (runAsNonRoot, UID 1000 等) からアクセスするために必須。
		// /etc/fuse.conf に user_allow_other が必要 (Dockerfile で設定済み)
		"-o", "allow_other",
		// umask=000: hostUsers: false 環境では host UID 0 はコンテナ内で unmapped UID に見える。
		// umask=000 (= rwxrwxrwx) にすることで所有者に関わらず任意 UID から読み書き可能にする。
		// uid/gid 固定は行わない（user namespace の UID 変換と干渉するため）。
		"-o", "umask=000",
		"-f", // フォアグラウンド（プロセス管理のため必須）
	}

	klog.Infof("sshfs 起動: %s@%s:%s -> %s", user, host, remotePath, targetPath)
	cmd := exec.Command("sshfs", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		os.Remove(keyFile.Name())
		return fmt.Errorf("sshfs 起動失敗: %w", err)
	}

	// マウント完了を待機（最大30秒）
	if err := waitForMount(targetPath, "fuse.sshfs", 30*time.Second); err != nil {
		cmd.Process.Kill()
		os.Remove(keyFile.Name())
		return fmt.Errorf("sshfs マウント待機タイムアウト: %w", err)
	}

	d.mu.Lock()
	d.mounts[targetPath] = &mountInfo{
		pid:       cmd.Process.Pid,
		keyFile:   keyFile.Name(),
		fsType:    "sshfs",
		mountedAt: time.Now(),
	}
	d.mu.Unlock()

	// プロセス終了を非同期で監視
	// クラッシュ時は d.mounts からエントリを削除する。
	// これにより次の NodePublishVolume が冪等性チェックで誤判定せず、
	// 再マウントを正常に試みられる。
	go func() {
		cmd.Wait()
		klog.Warningf("sshfs プロセス終了 (targetPath=%s)", targetPath)
		d.mu.Lock()
		delete(d.mounts, targetPath)
		d.mu.Unlock()
	}()

	return nil
}

// sanitize はファイルパスに使えない文字を置換する
func sanitize(s string) string {
	return strings.NewReplacer("/", "_", ":", "_", ".", "_").Replace(s)
}

// waitForMount は targetPath が指定の fstype でマウントされるまで待つ
func waitForMount(targetPath, fsType string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile("/proc/mounts")
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[1] == targetPath && fields[2] == fsType {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s に %s がマウントされるまでの待機タイムアウト", targetPath, fsType)
}
