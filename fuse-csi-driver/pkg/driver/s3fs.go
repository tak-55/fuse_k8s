package driver

import (
	"fmt"
	"time"

	"k8s.io/klog/v2"
)

// MountS3fs performs open(/dev/fuse) + mount() at targetPath,
// then starts a background UDS server that sends S3 credentials + fusefd to the sidecar.
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
func (d *Driver) MountS3fs(targetPath, emptyDir string, params map[string]string, secrets map[string]string) error {
	bucket := params["bucket"]
	endpoint := params["endpoint"]
	region := params["region"]
	if region == "" {
		region = "us-east-1"
	}

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

	// Open /dev/fuse and mount at targetPath
	fusefd, err := openAndMountFuse(targetPath)
	if err != nil {
		return err
	}

	fp := FuseParams{
		Type:        "s3fs",
		Bucket:      bucket,
		Endpoint:    endpoint,
		Region:      region,
		NoCheckCert: params["noCheckCert"],
	}
	fc := FuseCreds{
		AccessKey: secrets["access_key"],
		SecretKey: secrets["secret_key"],
	}

	stopFn, err := startFdServer(emptyDir, fp, fc, fusefd)
	if err != nil {
		closeFuse(fusefd, targetPath)
		return fmt.Errorf("UDS server 起動失敗: %w", err)
	}

	klog.Infof("s3fs fd-passing 設定完了: bucket=%s targetPath=%s", bucket, targetPath)

	d.mu.Lock()
	d.mounts[targetPath] = &mountInfo{
		fusefd:    fusefd,
		stopFdSrv: stopFn,
		fsType:    "s3fs",
		mountedAt: time.Now(),
	}
	d.mu.Unlock()
	return nil
}
