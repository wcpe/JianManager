//go:build unix

package grpc

import (
	"os"
	"strconv"
	"syscall"
)

func init() {
	ownerGroupFromFileInfo = ownerGroupUnix
}

// ownerGroupUnix 返回 uid/gid 数字串（避免 cgo 依赖 os/user）。
func ownerGroupUnix(st os.FileInfo) (owner, group string) {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys == nil {
		return "", ""
	}
	return strconv.FormatUint(uint64(sys.Uid), 10), strconv.FormatUint(uint64(sys.Gid), 10)
}
