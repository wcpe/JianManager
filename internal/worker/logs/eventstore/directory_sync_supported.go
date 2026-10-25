//go:build !windows

package eventstore

import (
	"errors"
	"os"
)

func syncDirectoryHandle(directory *os.File) error {
	err := directory.Sync()
	// 仅明确不支持目录同步的能力错误可豁免，真实介质、权限或关闭句柄错误仍必须传播。
	if errors.Is(err, os.ErrInvalid) {
		return nil
	}
	return err
}
