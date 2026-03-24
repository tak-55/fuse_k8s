package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/klog/v2"

	"github.com/scaleworx-inc/fuse_k8s/fuse-csi-driver/pkg/driver"
)

var (
	endpoint = flag.String("endpoint", "unix:/csi/csi.sock", "CSI endpoint")
	nodeID   = flag.String("nodeid", "", "Node ID (必須)")
	version  = "dev"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	if *nodeID == "" {
		fmt.Fprintln(os.Stderr, "--nodeid は必須です")
		os.Exit(1)
	}

	klog.Infof("fuse-csi-driver 起動: endpoint=%s nodeID=%s version=%s", *endpoint, *nodeID, version)

	drv := driver.New(*nodeID, version)
	if err := drv.Run(*endpoint); err != nil {
		klog.Fatalf("driver 実行エラー: %v", err)
	}
}
