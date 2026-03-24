package driver

import (
	"net"
	"os"
	"strings"
	"sync"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

const driverName = "fuse.csi.fuse-k8s.io"

// Driver は CSI gRPC サーバーの本体
type Driver struct {
	nodeID  string
	version string

	// targetPath → 実行中プロセス情報（マルチユーザー対応: 最大50エントリ並列管理）
	// アクセスは mu で保護。NodePublishVolume / NodeUnpublishVolume は並列呼び出しされる。
	mounts map[string]*mountInfo
	mu     sync.RWMutex
}

type mountInfo struct {
	pid       int
	keyFile   string    // 一時認証情報ファイルパス（クリーンアップ用）
	fsType    string    // "sshfs" or "s3fs"（ログ・デバッグ用）
	mountedAt time.Time
}

// New は Driver を初期化して返す
func New(nodeID, version string) *Driver {
	return &Driver{
		nodeID:  nodeID,
		version: version,
		mounts:  make(map[string]*mountInfo),
	}
}

// Run は CSI gRPC サーバーを起動する
func (d *Driver) Run(endpoint string) error {
	addr := strings.TrimPrefix(endpoint, "unix:")
	os.Remove(addr) // 古いソケットを削除

	l, err := net.Listen("unix", addr)
	if err != nil {
		return err
	}

	s := grpc.NewServer()
	csi.RegisterIdentityServer(s, d)
	csi.RegisterNodeServer(s, d)

	klog.Infof("CSI gRPC サーバー起動: %s", endpoint)
	return s.Serve(l)
}
