package driver

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"k8s.io/klog/v2"
)

// MountS3fs は s3fs を起動して targetPath にマウントする
//
// params:
//
//	bucket      = S3 バケット名
//	endpoint    = S3 エンドポイント URL（例: http://minio:9000）
//	region      = リージョン（デフォルト: us-east-1）
//	noCheckCert = "true" で TLS 証明書検証を無効化（自己署名証明書環境用）
//
// secrets:
//
//	access_key = S3 アクセスキー
//	secret_key = S3 シークレットキー
func (d *Driver) MountS3fs(targetPath string, params map[string]string, secrets map[string]string) error {
	bucket := params["bucket"]
	endpoint := params["endpoint"]
	region := params["region"]

	// 必須パラメータのバリデーション
	if bucket == "" {
		return fmt.Errorf("volumeAttributes.bucket が未設定です")
	}
	if endpoint == "" {
		return fmt.Errorf("volumeAttributes.endpoint が未設定です（例: http://minio:9000）")
	}
	if secrets["access_key"] == "" {
		return fmt.Errorf("nodePublishSecretRef の access_key が空です")
	}
	if secrets["secret_key"] == "" {
		return fmt.Errorf("nodePublishSecretRef の secret_key が空です")
	}

	if region == "" {
		region = "us-east-1"
	}
	noCheckCert := params["noCheckCert"] == "true"

	// passwd-s3fs を一時ファイルに書き出す（形式: access_key:secret_key）
	passwdFile, err := os.CreateTemp("", "fuse-csi-s3fs-passwd-*")
	if err != nil {
		return fmt.Errorf("passwd ファイル作成失敗: %w", err)
	}
	content := fmt.Sprintf("%s:%s", secrets["access_key"], secrets["secret_key"])
	if _, err := passwdFile.WriteString(content); err != nil {
		os.Remove(passwdFile.Name())
		return err
	}
	if err := passwdFile.Chmod(0600); err != nil {
		os.Remove(passwdFile.Name())
		return err
	}
	passwdFile.Close()

	args := []string{
		bucket,
		targetPath,
		"-o", "passwd_file=" + passwdFile.Name(),
		"-o", "url=" + endpoint,
		"-o", "endpoint=" + region,
		"-o", "use_path_request_style",
		// allow_other: ユーザーコンテナ (runAsNonRoot, UID 1000) からアクセスするために必須
		// /etc/fuse.conf に user_allow_other が必要 (Dockerfile で設定済み)
		"-o", "allow_other",
		// umask=000: hostUsers: false 環境での UID マッピング問題を回避し
		// 任意 UID から読み書き可能にする
		"-o", "umask=000",
		"-f", // フォアグラウンド（プロセス管理のため必須）
	}
	if noCheckCert {
		args = append(args, "-o", "no_check_certificate")
	}

	klog.Infof("s3fs 起動: bucket=%s -> %s (endpoint=%s)", bucket, targetPath, endpoint)
	cmd := exec.Command("s3fs", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		os.Remove(passwdFile.Name())
		return fmt.Errorf("s3fs 起動失敗: %w", err)
	}

	if err := waitForMount(targetPath, "fuse.s3fs", 30*time.Second); err != nil {
		cmd.Process.Kill()
		os.Remove(passwdFile.Name())
		return fmt.Errorf("s3fs マウント待機タイムアウト: %w", err)
	}

	d.mu.Lock()
	d.mounts[targetPath] = &mountInfo{
		pid:       cmd.Process.Pid,
		keyFile:   passwdFile.Name(),
		fsType:    "s3fs",
		mountedAt: time.Now(),
	}
	d.mu.Unlock()

	// プロセス終了を非同期で監視
	// クラッシュ時は d.mounts からエントリを削除する。
	// これにより次の NodePublishVolume が冪等性チェックで誤判定せず、
	// 再マウントを正常に試みられる。
	go func() {
		cmd.Wait()
		klog.Warningf("s3fs プロセス終了 (targetPath=%s)", targetPath)
		d.mu.Lock()
		delete(d.mounts, targetPath)
		d.mu.Unlock()
	}()

	return nil
}
