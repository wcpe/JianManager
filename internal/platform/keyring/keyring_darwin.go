//go:build darwin

package keyring

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// macOS 实现：调用系统自带的 security 命令操作 login keychain。
//
// 选 security 而非 cgo/Security.framework：前者零依赖、行为与 Keychain 访问面板一致，
// 且能触发系统的「是否允许 jm 访问」提示，符合用户预期。
type darwinKeyring struct{}

func backend() Keyring { return darwinKeyring{} }

// accountName 固定为 jm，同一 service+account 唯一确定一个条目。
const accountName = "jm"

// Set 写入或更新通用密码项。
//
// security 没有「更新」语义，-U 表示存在则更新。
func (darwinKeyring) Set(key, value string) error {
	k, err := normalizeKey(key)
	if err != nil {
		return err
	}
	cmd := exec.Command("security", "add-generic-password",
		"-s", serviceName,
		"-a", accountName,
		"-l", serviceName+"/"+k,
		"-w", value,
		"-U",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("写入 macOS 钥匙串失败: %w（输出: %s）", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Get 读取密码明文。
func (darwinKeyring) Get(key string) (string, error) {
	k, err := normalizeKey(key)
	if err != nil {
		return "", err
	}
	// 用 -l（标签）定位，因为 service+account 会被所有 host 共用。
	cmd := exec.Command("security", "find-generic-password",
		"-s", serviceName,
		"-l", serviceName+"/"+k,
		"-w",
	)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// security 在找不到条目时返回非零且 stderr 含 "could not be found"。
			if strings.Contains(string(exitErr.Stderr), "could not be found") {
				return "", ErrNotFound
			}
		}
		return "", fmt.Errorf("读取 macOS 钥匙串失败: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Delete 删除条目；不存在视为成功。
func (darwinKeyring) Delete(key string) error {
	k, err := normalizeKey(key)
	if err != nil {
		return err
	}
	cmd := exec.Command("security", "delete-generic-password",
		"-s", serviceName,
		"-l", serviceName+"/"+k,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := string(out)
		if strings.Contains(msg, "could not be found") {
			return nil
		}
		return fmt.Errorf("删除 macOS 钥匙串条目失败: %w（输出: %s）", err, strings.TrimSpace(msg))
	}
	return nil
}
