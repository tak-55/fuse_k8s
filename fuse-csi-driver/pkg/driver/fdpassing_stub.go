//go:build !linux

package driver

import "fmt"

type FuseParams struct {
	Type        string `json:"type"`
	Host        string `json:"host,omitempty"`
	User        string `json:"user,omitempty"`
	RemotePath  string `json:"remotePath,omitempty"`
	Port        string `json:"port,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	NoCheckCert string `json:"noCheckCert,omitempty"`
}

type FuseCreds struct {
	PrivateKey string `json:"private_key,omitempty"`
	AccessKey  string `json:"access_key,omitempty"`
	SecretKey  string `json:"secret_key,omitempty"`
}

const (
	fuseEmptyDirName = "fuse-fd"
	fuseSocketName   = "csi.sock"
	fuseParamsName   = "params.json"
	kubeletPodsDir   = "/var/lib/kubelet/pods"
)

func emptyDirHostPath(podUID string) string {
	return kubeletPodsDir + "/" + podUID + "/volumes/kubernetes.io~empty-dir/" + fuseEmptyDirName
}

func openAndMountFuse(targetPath string) (int, error) {
	return 0, fmt.Errorf("FUSE mount は Linux 専用です")
}

func startFdServer(emptyDir string, params FuseParams, creds FuseCreds, fusefd int) (func(), error) {
	return nil, fmt.Errorf("fd server は Linux 専用です")
}

func closeFuse(fusefd int, targetPath string) {}
