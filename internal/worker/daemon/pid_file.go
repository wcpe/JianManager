package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	ProbePort    int    `json:"probe_port,omitempty"` // ServerProbe /metrics 端口，供 Worker 重启恢复后心跳继续自采
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
//
// **必须原子写**（临时文件 + rename）：PID 文件会被写入**两次**——wrapper 监听就绪后写
// 「wrapper_pid + socket」，Java 拉起后再补写 java_pid（见 wrapper.go 的两次调用）。
// 裸 os.WriteFile 是「截断 → 写入」，中间存在文件长度 0 或半截 JSON 的窗口；此刻若读到，
// ReadRecord 会返回解析错误，而 RecoverDaemonInstances 对解析失败记录的处理是
// **删除 PID 文件并跳过**——一个正在服务的实例就被静默移出账本，再也不会被接管。
// rename 在同一文件系统内是原子的，读者要么看到旧记录、要么看到新记录，永不会看到半截。
func (p *PIDFile) WriteRecord(rec PIDRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("序列化 PID 记录失败: %w", err)
	}
	return writeFileAtomic(p.path, data, 0644)
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
	return writeFileAtomic(p.path, []byte(strconv.Itoa(pid)), 0644)
}

// writeFileAtomic 以「同目录临时文件 + fsync + rename」方式原子替换文件内容。
//
// 为何不用 os.WriteFile：它会先截断再写入，读者在窗口内会看到空文件或半截内容。
// 对 PID 文件而言这个窗口有真实代价——解析失败即被当成脏记录删除（见 WriteRecord 注释）。
// rename 在同目录（同一文件系统）内原子生效，读者只会看到完整的旧内容或完整的新内容。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("创建临时 PID 文件失败: %w", err)
	}
	tmpName := tmp.Name()
	// 任一步失败都清掉临时文件，避免在 PID 目录里堆积垃圾（该目录会被反复扫描）。
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入临时 PID 文件失败: %w", err)
	}
	// fsync：确保 rename 后内容已落盘，避免掉电时留下「文件存在但内容为空」。
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步临时 PID 文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时 PID 文件失败: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("设置 PID 文件权限失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换 PID 文件失败: %w", err)
	}
	tmpName = "" // 已成功 rename，勿再删除
	return nil
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
