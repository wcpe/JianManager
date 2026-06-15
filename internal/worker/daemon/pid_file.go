package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PIDFile PID 文件管理。
type PIDFile struct {
	path string
}

// NewPIDFile 创建 PID 文件管理器。
func NewPIDFile(path string) *PIDFile {
	return &PIDFile{path: path}
}

// Write 写入 PID 文件。
func (p *PIDFile) Write(pid int) error {
	return os.WriteFile(p.path, []byte(strconv.Itoa(pid)), 0644)
}

// Read 读取 PID 文件中的 PID。
func (p *PIDFile) Read() (int, error) {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return 0, fmt.Errorf("读取 PID 文件失败: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("解析 PID 失败: %w", err)
	}

	return pid, nil
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

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// 发送信号 0 检查进程是否存在
	err = proc.Signal(os.Signal(nil))
	return err == nil
}
