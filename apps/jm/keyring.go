package main

import (
	"errors"
	"fmt"

	"github.com/wcpe/JianManager/internal/platform/keyring"
)

// 令牌在系统钥匙串中的存取。
//
// 设计取舍：钥匙串不可用时**不降级为明文文件**，只提示改用 JM_TOKEN。
// 理由：CLI 令牌等价于 CP 全权访问，静默落盘明文等于把密钥摊在磁盘上。

// loadTokenFromKeyring 从钥匙串读令牌；不可用或不存在时返回 ok=false。
func loadTokenFromKeyring(hostKey string) (string, bool) {
	if hostKey == "" {
		return "", false
	}
	kr, err := keyring.Open()
	if err != nil {
		return "", false
	}
	tok, err := kr.Get(tokenKey(hostKey))
	if err != nil {
		return "", false
	}
	return tok, true
}

// saveTokenToKeyring 把令牌写入钥匙串。
//
// 返回的错误分成两类，调用方据此给出不同提示：
//   - keyring.ErrUnavailable：环境不支持，建议 JM_TOKEN
//   - 其它：写入失败（权限/钥匙串锁定）
func saveTokenToKeyring(hostKey, token string) error {
	kr, err := keyring.Open()
	if err != nil {
		return err
	}
	if err := kr.Set(tokenKey(hostKey), token); err != nil {
		return fmt.Errorf("写入系统钥匙串失败: %w", err)
	}
	return nil
}

// deleteTokenFromKeyring 从钥匙串删除令牌；不存在视为成功。
func deleteTokenFromKeyring(hostKey string) error {
	kr, err := keyring.Open()
	if err != nil {
		if errors.Is(err, keyring.ErrUnavailable) {
			return nil
		}
		return err
	}
	if err := kr.Delete(tokenKey(hostKey)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("删除系统钥匙串条目失败: %w", err)
	}
	return nil
}

// explainKeyringError 把钥匙串错误翻成可操作的中文提示。
func explainKeyringError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, keyring.ErrUnavailable) {
		return "当前环境没有可用的系统钥匙串。请改用环境变量：export JM_TOKEN=<令牌>"
	}
	return err.Error()
}
