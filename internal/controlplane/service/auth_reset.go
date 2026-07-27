package service

import (
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ResetUserPassword 本机应急重置指定用户密码并解锁账号（FR-333）。
// 仅供 CP 二进制 `reset-password` 子命令在本机调用——数据库仅 Control Plane 可读写
// 的所有权不变量因此保持（jmctl 按 ADR-041 不得直连 DB，故命令落在 CP 自身）。
// 重置同时将 Status 置回 Active：应急场景下密码丢失常伴随账号被锁，一次到位。
func ResetUserPassword(db *gorm.DB, username, plain string) (*model.User, error) {
	var user model.User
	if err := db.Where("username = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}

	if err := db.Model(&user).Updates(map[string]any{
		"password":     string(hashed),
		"status":       model.UserStatusActive,
		"auth_version": gorm.Expr("auth_version + ?", 1),
	}).Error; err != nil {
		return nil, fmt.Errorf("更新密码失败: %w", err)
	}
	return &user, nil
}

// GenerateResetPassword 生成 16 位随机密码（大小写字母+数字，crypto/rand）。
func GenerateResetPassword() (string, error) {
	const charset = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机密码失败: %w", err)
	}
	for i, b := range buf {
		buf[i] = charset[int(b)%len(charset)]
	}
	return string(buf), nil
}
