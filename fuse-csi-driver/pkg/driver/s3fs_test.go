package driver

import (
	"strings"
	"testing"
)

func TestMountS3fs_ValidationMissingBucket(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountS3fs("/tmp/test", "/tmp/emptydir", map[string]string{
		"endpoint": "http://minio:9000",
	}, map[string]string{
		"access_key": "key",
		"secret_key": "secret",
	})
	if err == nil {
		t.Error("bucket が空のときエラーが返るべき")
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("エラーに 'bucket' が含まれるべき: %v", err)
	}
}

func TestMountS3fs_ValidationMissingEndpoint(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountS3fs("/tmp/test", "/tmp/emptydir", map[string]string{
		"bucket": "my-bucket",
	}, map[string]string{
		"access_key": "key",
		"secret_key": "secret",
	})
	if err == nil {
		t.Error("endpoint が空のときエラーが返るべき")
	}
	if !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("エラーに 'endpoint' が含まれるべき: %v", err)
	}
}

func TestMountS3fs_ValidationMissingAccessKey(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountS3fs("/tmp/test", "/tmp/emptydir", map[string]string{
		"bucket":   "my-bucket",
		"endpoint": "http://minio:9000",
	}, map[string]string{
		"secret_key": "secret",
	})
	if err == nil {
		t.Error("access_key が空のときエラーが返るべき")
	}
	if !strings.Contains(err.Error(), "access_key") {
		t.Errorf("エラーに 'access_key' が含まれるべき: %v", err)
	}
}

func TestMountS3fs_ValidationMissingSecretKey(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountS3fs("/tmp/test", "/tmp/emptydir", map[string]string{
		"bucket":   "my-bucket",
		"endpoint": "http://minio:9000",
	}, map[string]string{
		"access_key": "key",
	})
	if err == nil {
		t.Error("secret_key が空のときエラーが返るべき")
	}
	if !strings.Contains(err.Error(), "secret_key") {
		t.Errorf("エラーに 'secret_key' が含まれるべき: %v", err)
	}
}
