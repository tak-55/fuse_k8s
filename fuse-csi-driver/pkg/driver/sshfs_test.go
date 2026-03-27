package driver

import (
	"strings"
	"testing"
)

func TestMountSshfs_ValidationMissingHost(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountSshfs("/tmp/test", "/tmp/emptydir", map[string]string{
		"user":       "user",
		"remotePath": "/remote",
	}, map[string]string{
		"private_key": "dummy-key",
	})
	if err == nil {
		t.Error("host が空のときエラーが返るべき")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("エラーに 'host' が含まれるべき: %v", err)
	}
}

func TestMountSshfs_ValidationMissingUser(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountSshfs("/tmp/test", "/tmp/emptydir", map[string]string{
		"host":       "myhost",
		"remotePath": "/remote",
	}, map[string]string{
		"private_key": "dummy-key",
	})
	if err == nil {
		t.Error("user が空のときエラーが返るべき")
	}
	if !strings.Contains(err.Error(), "user") {
		t.Errorf("エラーに 'user' が含まれるべき: %v", err)
	}
}

func TestMountSshfs_ValidationMissingRemotePath(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountSshfs("/tmp/test", "/tmp/emptydir", map[string]string{
		"host": "myhost",
		"user": "myuser",
	}, map[string]string{
		"private_key": "dummy-key",
	})
	if err == nil {
		t.Error("remotePath が空のときエラーが返るべき")
	}
	if !strings.Contains(err.Error(), "remotePath") {
		t.Errorf("エラーに 'remotePath' が含まれるべき: %v", err)
	}
}

func TestMountSshfs_ValidationMissingPrivateKey(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountSshfs("/tmp/test", "/tmp/emptydir", map[string]string{
		"host":       "myhost",
		"user":       "myuser",
		"remotePath": "/remote",
	}, map[string]string{})
	if err == nil {
		t.Error("private_key が空のときエラーが返るべき")
	}
	if !strings.Contains(err.Error(), "private_key") {
		t.Errorf("エラーに 'private_key' が含まれるべき: %v", err)
	}
}
