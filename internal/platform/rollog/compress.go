package rollog

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// compressLoop 是后台压缩协程：消费待压缩队列，把每个轮转档 gzip 后删除原文件。
//
// 设计约束：
//   - 永不回写、不抛错到调用方（这是后台任务，调用方早已返回）；错误走 OnError。
//   - 压缩完成后才删原文件：中途失败会同时保留 .log 和 .gz 的中间态，宁可多占盘不可丢数据。
//   - 每次压缩后执行一次保留清理，使 MaxBackups 对两种档位统一生效。
func (w *Writer) compressLoop() {
	defer w.wg.Done()
	for path := range w.compressQ {
		if err := w.compressOne(path); err != nil {
			w.cfg.OnError(err)
		}
		if err := w.applyRetention(); err != nil {
			w.cfg.OnError(fmt.Errorf("rollog: 清理过期轮转档失败: %w", err))
		}
	}
}

// compressOne 把单个轮转档压缩为 .gz。
//
// 用临时文件 + rename 发布：压缩到一半崩溃时不会留下一个“看起来完整、其实截断”的 .gz，
// 而是只留下一个 .tmp，下次启动的补压扫描会重新处理原 .log。
func (w *Writer) compressOne(path string) error {
	if filepath.Ext(path) == ".gz" {
		// 已经是压缩档（补压扫描可能重复入队），跳过。
		return nil
	}
	src, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 已被其他路径删除（例如手动清理），不算错误。
			return nil
		}
		return fmt.Errorf("rollog: 打开待压缩日志 %s 失败: %w", filepath.Base(path), err)
	}
	defer src.Close()

	tmpPath := path + ".gz.tmp"
	dst, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("rollog: 创建压缩文件 %s 失败: %w", filepath.Base(tmpPath), err)
	}

	zw := gzip.NewWriter(dst)
	if _, err := io.Copy(zw, src); err != nil {
		_ = zw.Close()
		_ = dst.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rollog: 压缩 %s 失败: %w", filepath.Base(path), err)
	}
	if err := zw.Close(); err != nil {
		_ = dst.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rollog: 关闭压缩流 %s 失败: %w", filepath.Base(path), err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rollog: 关闭压缩文件 %s 失败: %w", filepath.Base(path), err)
	}

	// 先发布 .gz，再删原文件。顺序反了（先删后发布）会在两者之间丢数据。
	gzPath := path + ".gz"
	if err := os.Rename(tmpPath, gzPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rollog: 发布压缩文件 %s 失败: %w", filepath.Base(gzPath), err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		// .gz 已就位，原文件删不掉只浪费空间，不影响正确性。
		return fmt.Errorf("rollog: 删除已压缩的原文件 %s 失败: %w", filepath.Base(path), err)
	}
	return nil
}
