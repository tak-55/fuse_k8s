// fusermount3-stub: libfuse3 から呼ばれる fusermount3 の代替バイナリ。
// 実際には mount を行わず、_FUSE_COMMFD ソケット経由で
// FUSE_PREOPEN_FD に指定された既存の fusefd を libfuse に返す。
// CSI driver が事前に mount(2) を完了しているため、再 mount 不要。
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func main() {
	commFDStr := os.Getenv("_FUSE_COMMFD")
	preopenFDStr := os.Getenv("FUSE_PREOPEN_FD")

	if commFDStr == "" || preopenFDStr == "" {
		// アンマウント呼び出し（fusermount3 -u /mnt/fuse）は _FUSE_COMMFD なしで呼ばれる。
		// マウントはすでに CSI driver が管理しているため何もせず正常終了する。
		fmt.Fprintln(os.Stderr, "fusermount3-stub: アンマウント呼び出し（または env 未設定）→ 正常終了")
		os.Exit(0)
	}

	commFD, err := strconv.Atoi(commFDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-stub: _FUSE_COMMFD parse 失敗: %v\n", err)
		os.Exit(1)
	}
	preopenFD, err := strconv.Atoi(preopenFDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-stub: FUSE_PREOPEN_FD parse 失敗: %v\n", err)
		os.Exit(1)
	}

	// libfuse の receive_fd() が期待するフォーマット: 1バイトデータ + SCM_RIGHTS
	sockFile := os.NewFile(uintptr(commFD), "commfd")
	conn, err := net.FileConn(sockFile)
	sockFile.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-stub: FileConn 失敗: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		fmt.Fprintln(os.Stderr, "fusermount3-stub: UnixConn 変換失敗")
		os.Exit(1)
	}

	rights := unix.UnixRights(preopenFD)
	if _, _, err := unixConn.WriteMsgUnix([]byte{0}, rights, nil); err != nil {
		fmt.Fprintf(os.Stderr, "fusermount3-stub: fusefd 送信失敗: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "fusermount3-stub: fusefd=%d を libfuse に送信\n", preopenFD)
}
