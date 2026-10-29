// Package taillog 读取日志文件的**尾部**若干行，供诊断接口使用。
//
// # 为什么单独抽包
//
// CP 读自己的日志文件、Worker 读自己的日志文件，两个场景需要**完全一样**的
// 边界语义（行数上限、字节上限、截断标记、文件不存在时的错误）。分成两份实现
// 必然在“末尾换行算不算一行”“超限从哪头截”这类细节上跑偏，而诊断接口的
// 用户正是拿它当证据排查事故的人。
//
// # 与 crashreport.Tail 的关系
//
// 截取算法直接复用 crashreport.Tail（已处理“末尾换行不计一行”等边界），
// 本包只额外负责“从哪里读”：只读文件尾部的有界区间，**不把整个文件读进内存**。
// 这一点是硬要求——事故现场日志可能是 35GB，诊断接口必须与文件大小无关。
package taillog

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/wcpe/JianManager/internal/worker/crashreport"
)

// 默认上限。与 crashreport 的环形缓冲口径一致（200 行 / 64KB），
// 使“面板看到的尾部”与“崩溃上报的尾部”是同一个量级。
const (
	DefaultMaxLines = crashreport.DefaultTailLines
	DefaultMaxBytes = crashreport.DefaultTailBytes
	// 硬上限：调用方传入的值只能收紧，不能放宽。
	//
	// 为什么要夹紧：这个接口经 HTTP 暴露给浏览器，若不设上限，
	// 一次请求就能把几百 MB 日志读进 CP 内存——把一个诊断功能变成 DoS 面。
	HardMaxLines = DefaultMaxLines
	HardMaxBytes = DefaultMaxBytes
)

// ErrFileMissing 表示日志文件不存在（尚未产生日志，或路径配错）。
var ErrFileMissing = errors.New("taillog: 日志文件不存在")

// Result 是一次尾部读取的结果。
type Result struct {
	// Content 是尾部原文。
	Content string
	// Truncated 表示因行数或字节上限被截断（UI 必须显式标注）。
	Truncated bool
	// FileBytes 是日志文件总大小，让运维知道“还有多少没看到”。
	FileBytes int64
	// Path 是实际读取的文件路径（供 UI 展示可复制的 grep 命令）。
	Path string
}

// Read 读取 path 的尾部。
//
// maxLines / maxBytes 传 0 表示用默认；传超过硬上限的值会被夹到硬上限（不报错）。
// 文件不存在返回 ErrFileMissing，其他 IO 错误原样包装。
func Read(path string, maxLines, maxBytes int) (Result, error) {
	if path == "" {
		return Result{}, fmt.Errorf("taillog: 路径为空")
	}
	maxLines = clamp(maxLines, DefaultMaxLines, HardMaxLines)
	maxBytes = clamp(maxBytes, DefaultMaxBytes, HardMaxBytes)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Path: path}, fmt.Errorf("%w: %s", ErrFileMissing, path)
		}
		return Result{Path: path}, fmt.Errorf("taillog: 打开日志文件失败: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Result{Path: path}, fmt.Errorf("taillog: 读取文件状态失败: %w", err)
	}
	total := info.Size()
	res := Result{Path: path, FileBytes: total}
	if total == 0 {
		return res, nil
	}

	// 只读尾部有界区间：这是本函数与文件大小无关的关键。
	// 多读一点交给 crashreport.Tail 按行裁剪，避免刚好切断行首导致丢半行。
	readFrom := int64(0)
	if total > int64(maxBytes) {
		readFrom = total - int64(maxBytes)
		res.Truncated = true
	}
	if _, err := f.Seek(readFrom, io.SeekStart); err != nil {
		return res, fmt.Errorf("taillog: 定位文件尾失败: %w", err)
	}

	buf := make([]byte, total-readFrom)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return res, fmt.Errorf("taillog: 读取日志尾部失败: %w", err)
	}
	buf = buf[:n]

	content := crashreport.Tail(buf, maxLines, maxBytes)
	// 行数超限也算截断（Tail 按行截取后长度会小于实际尾部）。
	if len(content) < len(buf) {
		res.Truncated = true
	}
	res.Content = content
	return res, nil
}

// clamp 把 v 夹到 [1, hard]。v<=0 时用 def。
func clamp(v, def, hard int) int {
	if v <= 0 {
		v = def
	}
	if v > hard {
		v = hard
	}
	return v
}
