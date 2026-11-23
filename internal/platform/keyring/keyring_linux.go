//go:build linux

package keyring

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Linux 实现：通过 secret-tool 访问 Secret Service（libsecret / GNOME Keyring / KWallet）。
//
// secret-tool 是 libsecret 的官方 CLI，几乎所有桌面环境都有；
// 容器/无 D-Bus 环境下会失败，此时返回 ErrUnavailable 让调用方提示改用 JM_TOKEN。
type linuxKeyring struct{}

func backend() Keyring {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		// 没有 secret-tool 即视为无可用后端。
		return nil
	}
	return linuxKeyring{}
}

// Set 写入或覆盖属性匹配的条目。
func (linuxKeyring) Set(key, value string) error {
	k, err := normalizeKey(key)
	if err != nil {
		return err
	}
	cmd := exec.Command("secret-tool", "store",
		"--label", serviceName+"/"+k,
		"service", serviceName,
		"host", k,
	)
	cmd.Stdin = strings.NewReader(value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("写入 Secret Service 失败: %w（输出: %s）", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Get 按属性查找并输出值。
func (linuxKeyring) Get(key string) (string, error) {
	k, err := normalizeKey(key)
	if err != nil {
		return "", err
	}
	cmd := exec.Command("secret-tool", "lookup", "service", serviceName, "host", k)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// secret-tool 在无匹配项时退出码 1，且通常无输出。
			if len(strings.TrimSpace(string(out))) == 0 {
				return "", ErrNotFound
			}
		}
		return "", fmt.Errorf("读取 Secret Service 失败: %w", err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "", ErrNotFound
	}
	return value, nil
}

// Delete 清除匹配的条目；不存在视为成功。
func (linuxKeyring) Delete(key string) error {
	k, err := normalizeKey(key)
	if err != nil {
		return err
	}
	cmd := exec.Command("secret-tool", "clear", "service", serviceName, "host", k)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// clear 在无匹配时不报错；其它错误才提示。
		msg := string(out)
		if strings.Contains(msg, "No such file") {
			return nil
		}
		return fmt.Errorf("删除 Secret Service 条目失败: %w（输出: %s）", err, strings.TrimSpace(msg))
	}
	return nil
}
