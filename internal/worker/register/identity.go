package register

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// identityFileName 本地节点身份文件名（存于数据根 etc/ 下，FR-080，见 ADR-020 §3）。
const identityFileName = "node-identity.json"

// Identity 是 Worker 本地持久化的节点身份（FR-080，见 ADR-020 §3）。
//
// 首次经 enrollment token 注册成功后，CP 换发的 node_uuid/node_secret 写入数据根
// etc/node-identity.json（0600）。Worker 重启时优先读该文件复用既有身份走重注册，
// 不重复消费一次性 enrollment token。文件含 node_secret，绝不回传日志。
type Identity struct {
	// NodeUUID CP 签发的节点 UUID。
	NodeUUID string `json:"nodeUuid"`
	// NodeSecret CP 签发的节点密钥（心跳鉴权用，敏感，不入日志）。
	NodeSecret string `json:"nodeSecret"`
	// NodeName 注册使用的节点名（重注册按 name 命中，需与首注册一致）。
	NodeName string `json:"nodeName"`
}

// IdentityPath 返回数据根下的身份文件绝对路径 <etcDir>/node-identity.json。
func IdentityPath(etcDir string) string {
	return filepath.Join(etcDir, identityFileName)
}

// LoadIdentity 从 etcDir 读取本地节点身份。文件不存在返回 (nil, nil)（首次安装的正常情形）。
// 文件存在但损坏（JSON 非法或缺关键字段）返回错误，由调用方决定回退策略。
func LoadIdentity(etcDir string) (*Identity, error) {
	path := IdentityPath(etcDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取节点身份文件失败: %w", err)
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("解析节点身份文件失败: %w", err)
	}
	if id.NodeUUID == "" || id.NodeSecret == "" {
		return nil, fmt.Errorf("节点身份文件缺少 nodeUuid/nodeSecret: %s", path)
	}
	return &id, nil
}

// SaveIdentity 把节点身份原子写入 etcDir 下的身份文件，权限 0600（含 node_secret，禁止他者读取）。
// 先写临时文件再 rename，避免写入中途崩溃留下半截文件。
func SaveIdentity(etcDir string, id *Identity) error {
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化节点身份失败: %w", err)
	}
	path := IdentityPath(etcDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入节点身份文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交节点身份文件失败: %w", err)
	}
	// rename 在某些平台会沿用源文件权限；显式收敛到 0600（含敏感 node_secret）。
	_ = os.Chmod(path, 0o600)
	return nil
}
