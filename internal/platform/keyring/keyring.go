// Package keyring 提供跨平台的系统钥匙串访问。
//
// 为什么不用明文文件：CLI 凭证等价于 CP 的全权访问，泄露后果远大于多写三套平台代码。
// 三个实现分别在 build tag 文件里：
//   - keyring_windows.go：Windows Credential Manager（wincred）
//   - keyring_darwin.go：macOS Keychain（security 命令）
//   - keyring_linux.go：Secret Service（secret-tool / libsecret）
//
// 不可用时返回 ErrUnavailable，由调用方决定是提示用户还是走 JM_TOKEN 环境变量旁路；
// 绝不静默降级为明文写入。
package keyring

import (
	"errors"
	"fmt"
)

// ErrUnavailable 表示当前平台/环境没有可用的钥匙串后端。
//
// 常见原因：容器内无 D-Bus、Linux 桌面未运行 secret service、macOS 无 security 命令。
var ErrUnavailable = errors.New("系统钥匙串不可用")

// ErrNotFound 表示该键不存在。
var ErrNotFound = errors.New("钥匙串中不存在该条目")

// Keyring 是钥匙串的最小接口。
//
// 只提供 Set/Get/Delete 三个操作：CLI 不需要枚举全部条目，
// 主机名由配置文件维护，钥匙串只负责「按 host 存一个 token」。
type Keyring interface {
	// Set 写入或覆盖某个键的值。
	Set(key, value string) error
	// Get 读取某个键的值；不存在返回 ErrNotFound。
	Get(key string) (string, error)
	// Delete 删除某个键；不存在视为成功（幂等）。
	Delete(key string) error
}

// serviceName 是所有平台共用的服务名，用于在钥匙串里归属到本程序。
const serviceName = "jm-cli"

// Available 返回当前平台是否有可用实现。
// 具体实现由 build tag 文件提供。
func Available() bool {
	return backend() != nil
}

// Open 返回当前平台的钥匙串实现。
//
// 无可用后端时返回 ErrUnavailable，调用方应提示用户使用 JM_TOKEN 环境变量。
func Open() (Keyring, error) {
	kr := backend()
	if kr == nil {
		return nil, fmt.Errorf("%w（可改用环境变量 JM_TOKEN 传入令牌）", ErrUnavailable)
	}
	return kr, nil
}

// normalizeKey 校验并归一化键名（主机名）。
func normalizeKey(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("钥匙串键名不能为空")
	}
	return key, nil
}
