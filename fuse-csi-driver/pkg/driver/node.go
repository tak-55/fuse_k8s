package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// NodePublishVolume は kubelet から呼ばれ、targetPath に FUSE ファイルシステムをマウントする。
// volumeContext["type"] で sshfs / s3fs を切り替える。
// 認証情報は nodePublishSecretRef で渡された Secret の内容が secrets に入る。
func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	targetPath := req.GetTargetPath()
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "targetPath が空です")
	}

	volCtx := req.GetVolumeContext()
	secrets := req.GetSecrets()
	fsType := volCtx["type"]

	klog.Infof("NodePublishVolume: targetPath=%s type=%s", targetPath, fsType)

	// 冪等性チェック: kubelet はリトライ時に同じ targetPath で再呼び出しする場合がある
	// 既にマウント済みであれば成功を返す（CSI spec 必須要件）
	d.mu.RLock()
	_, alreadyMounted := d.mounts[targetPath]
	d.mu.RUnlock()
	if alreadyMounted {
		klog.Infof("NodePublishVolume: %s は既にマウント済み（冪等）", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "targetPath 作成失敗: %v", err)
	}

	var err error
	switch fsType {
	case "sshfs":
		err = d.MountSshfs(targetPath, volCtx, secrets)
	case "s3fs":
		err = d.MountS3fs(targetPath, volCtx, secrets)
	default:
		return nil, status.Errorf(codes.InvalidArgument, "未知の type: %q (sshfs または s3fs を指定)", fsType)
	}

	if err != nil {
		return nil, status.Errorf(codes.Internal, "マウント失敗: %v", err)
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume は kubelet から呼ばれ、targetPath のマウントを解除する。
// fusermount3 でアンマウントし、プロセスと一時ファイルをクリーンアップする。
func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	targetPath := req.GetTargetPath()
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "targetPath が空です")
	}

	klog.Infof("NodeUnpublishVolume: targetPath=%s", targetPath)

	// アンマウント: fusermount3 を試み、失敗したら umount にフォールバック
	// （既にアンマウント済みでもエラーにしない）
	if out, err := exec.Command("fusermount3", "-u", targetPath).CombinedOutput(); err != nil {
		klog.Warningf("fusermount3 失敗、umount にフォールバック: %s: %v", out, err)
		if out2, err2 := exec.Command("umount", targetPath).CombinedOutput(); err2 != nil {
			klog.Warningf("umount も失敗（既にアンマウント済みの可能性）: %s: %v", out2, err2)
		}
	}

	// プロセスと一時認証情報ファイルをクリーンアップ
	d.mu.Lock()
	if info, ok := d.mounts[targetPath]; ok {
		if proc, err := os.FindProcess(info.pid); err == nil {
			proc.Kill()
		}
		if info.keyFile != "" {
			if err := os.Remove(info.keyFile); err != nil && !os.IsNotExist(err) {
				klog.Warningf("一時ファイル削除失敗: %s: %v", info.keyFile, err)
			}
		}
		delete(d.mounts, targetPath)
	}
	d.mu.Unlock()

	// targetPath ディレクトリを削除（なければ無視）
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		klog.Warningf("targetPath 削除失敗: %s: %v", targetPath, err)
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *Driver) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{}, nil
}

func (d *Driver) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: d.nodeID}, nil
}

// 以下は未使用だが CSI NodeServer インターフェース上必要なスタブ

func (d *Driver) NodeStageVolume(_ context.Context, _ *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, fmt.Sprintf("%s は未実装です", "NodeStageVolume"))
}

func (d *Driver) NodeUnstageVolume(_ context.Context, _ *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, fmt.Sprintf("%s は未実装です", "NodeUnstageVolume"))
}

func (d *Driver) NodeExpandVolume(_ context.Context, _ *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, fmt.Sprintf("%s は未実装です", "NodeExpandVolume"))
}

func (d *Driver) NodeGetVolumeStats(_ context.Context, _ *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, fmt.Sprintf("%s は未実装です", "NodeGetVolumeStats"))
}
