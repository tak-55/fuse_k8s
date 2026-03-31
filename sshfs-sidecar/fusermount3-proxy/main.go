// fusermount3-proxy: libfuse3 から呼ばれる fusermount3 の代替バイナリ。
// CSI DaemonSet の UDS ソケットに接続して fusefd を受け取り、
// _FUSE_COMMFD ソケット経由で libfuse に返す。
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func main() {
	// アンマウント呼び出し（_FUSE_COMMFD なし）→ 何もせず正常終了
	commFDStr := os.Getenv("_FUSE_COMMFD")
	if commFDStr == "" {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: アンマウント呼び出し → 正常終了")
		os.Exit(0)
	}

	socketPath := os.Getenv("FUSERMOUNT3PROXY_FDPASSING_SOCKPATH")
	if socketPath == "" {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: FUSERMOUNT3PROXY_FDPASSING_SOCKPATH が未設定")
		os.Exit(1)
	}

	commFD, err := strconv.Atoi(commFDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: _FUSE_COMMFD parse 失敗: %v\n", err)
		os.Exit(1)
	}

	// CSI UDS ソケットに接続して fusefd を SCM_RIGHTS で受信
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: CSI UDS 接続失敗 (%s): %v\n", socketPath, err)
		os.Exit(1)
	}
	defer conn.Close()

	unixConn := conn.(*net.UnixConn)
	buf := make([]byte, 500)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unixConn.ReadMsgUnix(buf, oob)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: fd 受信失敗: %v\n", err)
		os.Exit(1)
	}

	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) == 0 {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: SCM_RIGHTS parse 失敗")
		os.Exit(1)
	}
	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil || len(fds) == 0 {
		fmt.Fprintln(os.Stderr, "fusermount3-proxy: UnixRights parse 失敗")
		os.Exit(1)
	}
	fuseFd := fds[0]
	fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd=%d を CSI から受信\n", fuseFd)

	// libfuse の _FUSE_COMMFD ソケットに fusefd を SCM_RIGHTS で返す
	commFdFile := os.NewFile(uintptr(commFD), "commfd")
	commConn, err := net.FileConn(commFdFile)
	commFdFile.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: commfd FileConn 失敗: %v\n", err)
		os.Exit(1)
	}
	defer commConn.Close()

	commUnix := commConn.(*net.UnixConn)
	rights := unix.UnixRights(fuseFd)
	if _, _, err := commUnix.WriteMsgUnix([]byte{0}, rights, nil); err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd 送信失敗: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "fusermount3-proxy: fusefd=%d を libfuse に送信完了\n", fuseFd)
}
