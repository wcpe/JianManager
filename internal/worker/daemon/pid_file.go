package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PIDRecord 记录 daemon wrapper 的恢复信息。
// Worker 重启后读取此文件：wrapper pid 存活则 reconnect socket 恢复管理，
// 否则清理文件。java pid 用于诊断/展示。
type PIDRecord struct {
	WrapperPID   int    `json:"wrapper_pid"`
	JavaPID      int    `json:"java_pid"`
	SocketAddr   string `json:"socket_addr"` // Unix Socket 路径或 Named Pipe 名称
	InstanceUUID string `json:"instance_uuid"`
	WorkDir      string `json:"work_dir"`             // 实例工作目录，供 Worker 重启恢复后做文件/配置操作
	ProbePort    int    `json:"probe_port,omitempty"` // ServerProbe /metrics 端口，供 Worker 重启恢复后心跳继续自采（FR-060）
}

// PIDFile PID 文件管理。
// 旧版只存裸 PID 数字，新版存 JSON（PIDRecord）。Read 同时兼容两种格式。
type PIDFile struct {
	path string
}

// NewPIDFile 创建 PID 文件管理器。
func NewPIDFile(path string) *PIDFile {
	return &PIDFile{path: path}
}

// Path 返回 PID 文件路径。
func (p *PIDFile) Path() string { return p.path }

// WriteRecord 写入完整恢复记录（JSON）。
func (p *PIDFile) WriteRecord(rec PIDRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("序列化 PID 记录失败: %w", err)
	}
	return os.WriteFile(p.path, data, 0644)
}

// ReadRecord 读取完整恢复记录。
func (p *PIDFile) ReadRecord() (*PIDRecord, error) {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return nil, fmt.Errorf("读取 PID 文件失败: %w", err)
	}

	// 兼容旧版裸 PID 数字格式
	trimmed := strings.TrimSpace(string(data))
	if pid, err := strconv.Atoi(trimmed); err == nil {
		return &PIDRecord{WrapperPID: pid}, nil
	}

	var rec PIDRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("解析 PID 记录失败: %w", err)
	}
	return &rec, nil
}

// Write 写入 PID 文件（仅 wrapper pid，旧版兼容）。
func (p *PIDFile) Write(pid int) error {
	return os.WriteFile(p.path, []byte(strconv.Itoa(pid)), 0644)
}

// Read 读取 PID 文件中的 PID（优先返回 wrapper pid）。
func (p *PIDFile) Read() (int, error) {
	rec, err := p.ReadRecord()
	if err != nil {
		return 0, err
	}
	return rec.WrapperPID, nil
}

// Remove 删除 PID 文件。
func (p *PIDFile) Remove() error {
	if err := os.Remove(p.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsProcessAlive 检查进程是否存活。
func (p *PIDFile) IsProcessAlive() bool {
	pid, err := p.Read()
	if err != nil {
		return false
	}
	return IsPIDAlive(pid)
}

// IsPIDAlive 检查指定 PID 的进程是否存活。
// 跨平台实现见 pid_alive_unix.go / pid_alive_windows.go
// （Windows 上 signal-0 不可用，改用 OpenProcess 探测）。
