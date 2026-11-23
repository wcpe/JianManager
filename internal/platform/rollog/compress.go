package rollog

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 压缩相关的文件名后缀。集中定义：命名占领检查（nextRotatedName）与压缩实现
// 必须看同一套后缀，否则又会出现「一边以为没人占、一边已经写了一半」的撞车。
const (
	compressSuffix = ".gz"
	tmpSuffix      = ".tmp"
)

// maintTask 是一次后台维护任务：压缩若干轮转档，然后统一做一次保留清理。
//
// # 为什么压缩与清理绑在一起
//
// 两者都是“轮转之后要做、但绝不能挡住写路径”的低频工作；合并在同一队列里
// 保证它们按轮转顺序串行执行，不会出现“旧的还没压完，新的已经开始删它”的竞态。
//
// 但**清理并不依赖压缩**：DisableCompress 时 compressPaths 为空，
// 任务仍然会做保留清理。这正是旧实现（只在压缩协程里顺带清理）的缺陷。
type maintTask struct {
	compressPaths []string
}

// maintenanceLoop 是后台维护协程：消费维护队列。
//
// 设计约束：
//   - 永不回写、不抛错到调用方（这是后台任务，调用方早已返回）；错误走 OnError。
//   - 压缩完成后才删原文件：中途失败会同时保留 .log 和 .gz 的中间态，宁可多占盘不可丢数据。
//   - **每次都执行保留清理**：它不依赖压缩是否开启（见 Writer.maintQ 的说明）。
func (w *Writer) maintenanceLoop() {
	defer w.wg.Done()
	for task := range w.maintQ {
		// 先清理后压缩：保留策略可能把某份轮转档直接删掉，
		// 而压缩一份马上要删的 1GB 文件是纯浪费（机械盘上是分钟级 IO）。
		// 两者都基于档名时间戳判定，先后不影响最终留下哪些文件。
		if err := w.applyRetention(); err != nil {
			w.cfg.OnError(fmt.Errorf("rollog: 清理过期轮转档失败: %w", err))
		}
		// 清扫已无原文件的压缩中间文件：它不在 listRotated 的管辖范围里，
		// 必须单独扫（详见 sweepOrphanTmp 的说明）。
		w.sweepOrphanTmp()
		for _, path := range task.compressPaths {
			if err := w.compressOne(path); err != nil {
				w.cfg.OnError(err)
			}
		}
	}
}

func (w *Writer) compressOne(path string) error {
	if filepath.Ext(path) == compressSuffix {
		// 已经是压缩档（补压扫描可能重复入队），跳过。
		return nil
	}

	tmpPath := path + compressSuffix + tmpSuffix

	src, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 原文件已被删除（保留策略、人工清理）：这次的 .tmp 不会有完成品了，
			// 一并清掉，否则它就是一个永远不被引用的孤儿。
			w.removeOrphanTmp(tmpPath)
			return nil
		}
		return fmt.Errorf("rollog: 打开待压缩日志 %s 失败: %w", filepath.Base(path), err)
	}

	dst, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		// 源文件必须在这里显式关闭：Windows 上不关闭会连临时目录都清理不掉。
		_ = src.Close()
		return fmt.Errorf("rollog: 创建压缩文件 %s 失败: %w", filepath.Base(tmpPath), err)
	}

	zw := gzip.NewWriter(dst)
	if _, err := io.Copy(zw, src); err != nil {
		_ = zw.Close()
		_ = dst.Close()
		_ = src.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rollog: 压缩 %s 失败: %w", filepath.Base(path), err)
	}
	if err := zw.Close(); err != nil {
		_ = dst.Close()
		_ = src.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rollog: 关闭压缩流 %s 失败: %w", filepath.Base(path), err)
	}
	if err := dst.Close(); err != nil {
		_ = src.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rollog: 关闭压缩文件 %s 失败: %w", filepath.Base(path), err)
	}

	// 先发布 .gz，再删原文件。顺序反了（先删后发布）会在两者之间丢数据。
	gzPath := path + compressSuffix
	if err := os.Rename(tmpPath, gzPath); err != nil {
		_ = src.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rollog: 发布压缩文件 %s 失败: %w", filepath.Base(gzPath), err)
	}

	// # 必须先把源文件关掉再删除（不能用 defer）
	//
	// Go 在 Windows 上用 CreateFile 打开文件时只申请 FILE_SHARE_READ|FILE_SHARE_WRITE，
	// **不含 FILE_SHARE_DELETE**，所以「仍持有句柄时删除」在 Windows 上必然失败并返回
	// ERROR_SHARING_VIOLATION。原实现把 close 推迟到函数返回（defer），而 Remove 发生在
	// 返回之前，于是 Windows 上每份轮转档的 .log 都删不掉：.gz 已生成、原文件却永远残留，
	// 磁盘占用翻倍，且 listRotated 会把同一份数据当成两个文件参与保留判定。
	// Linux 上「删除已打开的文件」合法（目录项立刻消失），因此该缺陷只在 Windows 暴露。
	if closeErr := src.Close(); closeErr != nil {
		// 关闭失败不阻断删除尝试：句柄已失效，删除通常仍可进行。
		w.cfg.OnError(fmt.Errorf("rollog: 关闭待压缩日志 %s 失败（继续删除）: %w", filepath.Base(path), closeErr))
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		// .gz 已就位，原文件删不掉只浪费空间，不影响正确性。
		return fmt.Errorf("rollog: 删除已压缩的原文件 %s 失败: %w", filepath.Base(path), err)
	}
	return nil
}

// removeOrphanTmp 删掉一个已无所属的压缩中间文件。
//
// 它不算错误也不上报：这个文件本来就只是“写到一半的产物”，它的原 .log 已经没了，
// 留着也没人能恢复出什么。静默清掉即可。
func (w *Writer) removeOrphanTmp(tmpPath string) {
	if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
		w.cfg.OnError(fmt.Errorf("rollog: 清理残余压缩中间文件 %s 失败: %w", filepath.Base(tmpPath), err))
	}
}

// sweepOrphanTmp 清扫目录里所有“已无原文件”的 `.gz.tmp` 孤儿。
//
// # 为什么单靠补压路径不够
//
// 补压任务是由“当前存在的 .log”驱动的；而孤儿 .tmp 的典型成因恰恰是 **.log 已经不在了**
// （先崩在压缩中途，下次启动时保留策略先跑并把超期的 .log 删了——维护顺序是“先清理后压缩”）。
// 这种档不会进补压队列，只能靠一次目录级扫描找到。
//
// 调用时机：Open 的首次维护任务。每次启动扫一次，成本等于一次 ReadDir。
func (w *Writer) sweepOrphanTmp() {
	matches, err := filepath.Glob(filepath.Join(w.dir, w.base+"-*"+w.ext+compressSuffix+tmpSuffix))
	if err != nil {
		w.cfg.OnError(fmt.Errorf("rollog: 扫描残留压缩中间文件失败: %w", err))
		return
	}
	for _, tmpPath := range matches {
		// 对应的 .log 与 .gz 都不在 ⇒ 它是孤儿。
		// 只看 .log 就够：若 .gz 存在而 .log 不在，说明压缩已完成，.tmp 只是发布后的残迹，同样该删。
		src := strings.TrimSuffix(tmpPath, compressSuffix+tmpSuffix)
		if _, err := os.Stat(src); err == nil {
			// 原文件还在：留给补压路径处理（会覆盖并发布这个 .tmp）。
			continue
		}
		w.removeOrphanTmp(tmpPath)
	}
}
