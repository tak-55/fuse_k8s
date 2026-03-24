package driver

import (
	"context"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
)

// GetPluginInfo は CSI ドライバー名とバージョンを返す
func (d *Driver) GetPluginInfo(_ context.Context, _ *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{
		Name:          driverName,
		VendorVersion: d.version,
	}, nil
}

// GetPluginCapabilities は Node-only driver としての capability を返す
// Controller 機能（PVC 管理等）は不要のため空で返す
func (d *Driver) GetPluginCapabilities(_ context.Context, _ *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{}, nil
}

// Probe はヘルスチェック用。常に成功を返す。
func (d *Driver) Probe(_ context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}
