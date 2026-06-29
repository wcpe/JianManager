package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteWorkerYML 原子写出 worker.yml（FR-222，见 ADR-051）。
//
// 字段与 install-worker.sh 写出的一组保持一致（name / control_plane / data_dir / grpc.port /
// ws.port / log）。**enrollment token 绝不写入**（一次性凭据不留盘，沿用 ADR-020 §2.3）；
// data_dir 仅在显式给出时写（缺省留空 = ./data，不把派生绝对路径钉死进文件）。
//
// 直接拼 YAML 文本（而非引第三方 marshaler）：字段少且固定、需带说明注释、对值做最小转义，
// 手写更可控、产物与脚本字节风格一致。
func WriteWorkerYML(path string, in *Inputs) error {
	name := in.NodeName
	if name == "" {
		name = defaultNodeName()
	}

	var b strings.Builder
	b.WriteString("# 由 worker setup 生成（FR-222，见 ADR-051）。enrollment token 不写入本文件。\n")
	fmt.Fprintf(&b, "name: %s\n", yamlScalar(name))
	fmt.Fprintf(&b, "control_plane: %s\n", yamlScalar(in.ControlPlane))
	if strings.TrimSpace(in.DataDir) != "" {
		fmt.Fprintf(&b, "data_dir: %s\n", yamlScalar(in.DataDir))
	}
	b.WriteString("grpc:\n")
	fmt.Fprintf(&b, "  port: %d\n", in.GRPCPort)
	b.WriteString("ws:\n")
	fmt.Fprintf(&b, "  port: %d\n", in.WSPort)
	b.WriteString("log:\n")
	b.WriteString("  level: info\n")
	b.WriteString("  format: json\n")

	return atomicWriteFile(path, []byte(b.String()), 0o644)
}

// atomicWriteFile 原子写文件：先写临时文件再 rename，避免写一半崩溃留坏配置。
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("写临时配置失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交配置文件失败: %w", err)
	}
	return nil
}

// defaultNodeName 节点名留空时的回退（node-<hostname>，与 install-worker.sh 一致）。
func defaultNodeName() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "node-local"
	}
	return "node-" + h
}

// yamlScalar 对标量值做最小安全引用：含 YAML 特殊起始/字符时加双引号并转义。
// setup 写出的值（host:port、节点名、路径）多含 : 等字符，统一加引号最稳妥、可读。
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}
