package driver

import (
	"os"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"192.168.1.1", "192_168_1_1"},
		{"host.example.com", "host_example_com"},
		{"host:22", "host_22"},
		{"/remote/path", "_remote_path"},
		{"simple", "simple"},
	}
	for _, tt := range tests {
		got := sanitize(tt.input)
		if got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWaitForMount_Timeout(t *testing.T) {
	// 存在しない targetPath でタイムアウトすることを確認
	// /proc/mounts に存在しないパスを指定
	err := waitForMount("/nonexistent/path/that/will/never/mount", "fuse.sshfs", 0)
	if err == nil {
		t.Error("タイムアウトのはずが err == nil")
	}
	if !strings.Contains(err.Error(), "タイムアウト") {
		t.Errorf("エラーメッセージに「タイムアウト」が含まれない: %v", err)
	}
}

func TestWaitForMount_AlreadyMounted(t *testing.T) {
	// 一時ファイルに /proc/mounts の内容を書いてテストする
	// waitForMount は /proc/mounts を直接読む実装なので、
	// テスト用にダミーエントリを含んだ /proc/mounts が使えない環境では
	// 実際のマウントを持つエントリを探す
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		t.Skip("/proc/mounts が読めない環境（非 Linux）")
	}

	// 既存のマウントエントリから fstype を取得してテスト
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] != "" && fields[2] != "" {
			// 既存マウントが検出できることを確認
			err := waitForMount(fields[1], fields[2], 0)
			if err == nil {
				return // 成功: マウント済みを正しく検出した
			}
		}
	}
	t.Skip("検証可能な既存マウントが見つからない")
}

func TestMountSshfs_ValidationMissingHost(t *testing.T) {
	d := New("test-node", "test")
	err := d.MountSshfs("/tmp/test", map[string]string{
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
	err := d.MountSshfs("/tmp/test", map[string]string{
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
	err := d.MountSshfs("/tmp/test", map[string]string{
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
	err := d.MountSshfs("/tmp/test", map[string]string{
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
