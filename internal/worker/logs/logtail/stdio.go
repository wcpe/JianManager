package logtail

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StdioBridge 把实例进程的 stdout/stderr 落成**按天文件**，交给 tailer 采集。
//
// # 为什么需要它
//
// 新采集器的模型是「文件是唯一真相」：它只会扫文件。而 STDIO_PRIMARY 类实例
// （代理 / 通用二进制 / Beacon）的日志**只存在于进程管道里**，它们不写 latest.log。
// 若无桥接，这类实例在新采集器下永远没有日志。
//
// # 为什么不直接写 VL
//
// 直接写 VL 意味着两个写入路径：一条走 tailer（带 offset / 重启续读 / 轮转跟随），
// 另一条直连 VL（无任何身份与重读保证）。两条路径的字段、时间、去重口径会立刻分叉——
// 而「同一概念多处实现」是本仓反复踩过的坑。
//
// 落成文件后，stdio 与文件源走**完全相同**的后续链路，没有第二套实现。
// 代价是多一次磁盘写，但这是采集系统里最便宜的一环。
//
// # 为什么按天分文件
//
// 单文件无限增长会让重启时的「从文件头读」变成灾难（读几百 MB 重投）。
// 按天切分后，单文件有界，而历史天的文件仍可补采。文件名用 UTC 日，与分区键
// （UTC 日）同口径，不做本地时区偏移。
type StdioBridge struct {
	root string

	mu      sync.Mutex
	handles map[string]*os.File // key: uuid/stream
	days    map[string]string   // key: uuid/stream → 当前文件对应的 UTC 日
}

// NewStdioBridge 创建桥接器。root 为空时所有写入都被丢弃（未启用）。
func NewStdioBridge(root string) *StdioBridge {
	return &StdioBridge{root: strings.TrimSpace(root), handles: map[string]*os.File{}, days: map[string]string{}}
}

// Root 返回桥接根目录（供 glob 拼接）。
func (s *StdioBridge) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Paths 返回桥接目录的采集 glob（三层：uuid / UTC 日 / stream.log）。
func (s *StdioBridge) Paths() []string {
	if s == nil || s.root == "" {
		return nil
	}
	return []string{filepath.Join(s.root, "*", "*", "*.log")}
}

// Append 追写一段进程输出。
//
// # 为什么不需要先有绑定
//
// 文件路径只由 `uuid + UTC 日 + stream` 决定，与「这个 uuid 属于哪个日志目标」无关。
// 所以**先写、后绑定**是安全的：绑定到达后 tailer 会从 offset 0（位点表里没记录）
// 把这段读出来。反过来说，先要求绑定会丢掉「进程已启动但 CP 登记还没到」那段输出。
func (s *StdioBridge) Append(uuid, stream string, data []byte) error {
	if s == nil || s.root == "" || len(data) == 0 {
		return nil
	}
	if uuid == "" || (stream != "stdout" && stream != "stderr") {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	day := time.Now().UTC().Format("2006-01-02")
	key := uuid + "/" + stream
	fh, ok := s.handles[key]
	if !ok || s.days[key] != day {
		if ok {
			_ = fh.Close()
			delete(s.handles, key)
		}
		dir := filepath.Join(s.root, uuid, day)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			slog.Warn("logtail: 创建 stdio 落盘目录失败", "dir", dir, "error", err)
			return err
		}
		path := filepath.Join(dir, stream+".log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			slog.Warn("logtail: 打开 stdio 落盘文件失败", "path", path, "error", err)
			return err
		}
		s.handles[key], s.days[key] = f, day
		fh = f
	}
	if _, err := fh.Write(data); err != nil {
		slog.Warn("logtail: 写 stdio 落盘文件失败", "uuid", uuid, "stream", stream, "error", err)
		return err
	}
	return nil
}

// Close 关闭所有句柄。
func (s *StdioBridge) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for k, f := range s.handles {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.handles, k)
	}
	return firstErr
}
