// Package rollog 提供进程内自轮转的日志文件写入器。
//
// # 为什么需要它
//
// 此前 CP / Worker 的日志文件完全由**外部重定向**产生（systemd `StandardOutput=append:`
// 或 `nohup >> file`），进程手里只有 stdout/stderr 的 fd，无法自己 reopen。
// 这种形态下 logrotate 只能使用 `copytruncate`——而 copytruncate 每次轮转要
// **完整读一遍 + 写一遍**整个文件。2026-10-27 真机事故里 `worker.out` 涨到 35GB，
// 轮转一次就是 35GB 的读写；在机械盘上这是几十秒到分钟级的停顿。
//
// 本包让进程自己持有日志文件并自己完成轮转：
//
//	活跃文件：<name>.log
//	轮转后：  <name>-20261029-153000.log       ← rename 得来，O(1)，零复制
//	压缩后：  <name>-20261029-153000.log.gz    ← 后台异步压缩
//
// 关键性质：
//
//   - **轮转是一次 rename**（同目录内），不复制任何字节。这是相对 copytruncate 的根本改进。
//   - **压缩异步**：轮转线程只做 rename + 新建 fd 就返回，压缩在独立 goroutine 里进行，
//     不阻塞任何日志调用方（log4j 默认同步压缩会卡写线程，这里刻意不照抄）。
//   - **崩溃可恢复**：进程在 rename 之后、压缩之前死掉，只会留下一个未压缩的 .log，
//     下次 Open 时会扫描并补压——不丢任何已写入内容。
//   - **断点续写**：已存在的活跃文件不会被截断，打开后从当前大小继续追加。
//
// 本包**不依赖 slog**（否则日志组件自身的错误日志会递归回自己）。错误经 OnError
// 回调上报，调用方应把它写到 stderr 或别处，不要写回本 writer。
package rollog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 默认参数。调用方通常只覆盖 Path。
const (
	// DefaultMaxSizeBytes 单个活跃文件触发轮转的大小（1 GiB）。
	// 与既有 logrotate 配置（configs/logrotate.d/jianmanager 的 size 1G）保持一致，
	// 使“进程内轮转”与“外部兜底轮转”在同一阈值上，不会互相抢先。
	DefaultMaxSizeBytes int64 = 1 << 30
	// DefaultMaxBackups 轮转档保留数量（含未压缩与已压缩）。
	DefaultMaxBackups = 5
	// compressQueueSize 待压缩队列容量。轮转是低频事件，16 足够；
	// 队列满时宁可放弃压缩（留一个未压缩 .log）也不阻塞写路径。
	compressQueueSize = 16
	// defaultStatInterval 是“活跃文件是否被外部删除”的检测窗口。
	// 1s 是两方面的折中：偏长会让自愈变慢（期间日志写进已删除 inode），
	// 偏短会把 os.Stat 变成高频系统调用。日志是秒级可接受的路径。
	defaultStatInterval = time.Second
)

// Config 是 Writer 的配置。
type Config struct {
	// Path 是活跃日志文件的完整路径（如 /opt/jm/data/log/worker.log）。
	// 父目录不存在时会自动创建。
	Path string
	// MaxSizeBytes 单文件触发轮转的字节数。<=0 表示用 DefaultMaxSizeBytes。
	MaxSizeBytes int64
	// MaxBackups 保留的轮转档数量（未压缩与已压缩一起计数）。<=0 表示用 DefaultMaxBackups；
	// 负值表示不限（不推荐，会导致磁盘无限增长）。
	MaxBackups int
	// Compress 是否对轮转档做异步 gzip。默认 true 由 New 填充（零值 Config 走默认开启）。
	DisableCompress bool
	// OnError 接收轮转/压缩/清理过程中的非致命错误。
	//
	// 为什么是回调而不是返回值：Write 的调用方是 slog handler，它无法处理这类错误，
	// 而把错误吞掉又会让“磁盘满导致轮转失败”这类事故无声无息。回调让调用方
	// （main）把错误直接写到 stderr，且刻意**不经过 slog**，避免递归。
	OnError func(error)
}

// Writer 是实现了 io.Writer 的自轮转日志写入器。并发安全。
//
// Write 只做“写 + 判大小”两件事，锁内不含任何 IO 等待（除写入本身）。
// 轮转与压缩都在锁外/独立 goroutine 完成。
type Writer struct {
	cfg Config

	dir  string // 日志所在目录
	base string // 不含扩展名的文件名，如 worker
	ext  string // 扩展名，如 .log

	mu     sync.Mutex
	file   *os.File
	size   int64
	closed bool

	// lastStat / statInterval 控制“文件是否被外部删除”的检测频率（见 reopenIfVanishedLocked）。
	lastStat     time.Time
	statInterval time.Duration

	// compressQ 待压缩的轮转档路径。
	compressQ chan string
	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

// Open 打开（或创建）日志文件并返回 Writer。
//
// 已存在的活跃文件不会被截断或清空，而是从当前大小继续追加——这样进程重启后
// 历史日志仍在同一文件里，符合运维 tail -f 的直觉。
func Open(cfg Config) (*Writer, error) {
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, fmt.Errorf("rollog: Path 不能为空")
	}
	if cfg.MaxSizeBytes <= 0 {
		cfg.MaxSizeBytes = DefaultMaxSizeBytes
	}
	if cfg.MaxBackups == 0 {
		cfg.MaxBackups = DefaultMaxBackups
	}
	if cfg.OnError == nil {
		cfg.OnError = func(error) {}
	}

	dir := filepath.Dir(cfg.Path)
	name := filepath.Base(cfg.Path)
	ext := filepath.Ext(name)
	if ext == "" {
		ext = ".log"
	}
	base := strings.TrimSuffix(name, ext)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("rollog: 创建日志目录失败: %w", err)
	}

	w := &Writer{
		cfg:          cfg,
		dir:          dir,
		base:         base,
		ext:          ext,
		done:         make(chan struct{}),
		statInterval: defaultStatInterval,
	}
	if !cfg.DisableCompress {
		w.compressQ = make(chan string, compressQueueSize)
	}

	if err := w.openActiveLocked(); err != nil {
		return nil, err
	}

	if w.compressQ != nil {
		w.wg.Add(1)
		go w.compressLoop()
		// 启动时补压：上次进程可能在 rename 之后、压缩之前退出，
		// 留下未压缩的轮转档。这里把它们扫出来重新入队。
		w.enqueueExistingRotated()
	}
	return w, nil
}

// Path 返回活跃日志文件的路径。诊断接口据此定位文件。
func (w *Writer) Path() string { return w.cfg.Path }

// Write 实现 io.Writer。
//
// 写入成功后若跨过大小阈值，立即在锁内完成轮转（只有 rename，O(1)）。
// 轮转失败不影响已经成功的那次写入的返回结果——日志宁可继续写进大文件，
// 也不能因为轮转失败而让调用方以为写失败了。
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return 0, os.ErrClosed
	}

	// 自愈：文件可能被外部删除（见 reopenIfVanishedLocked 的说明）。
	w.reopenIfVanishedLocked()
	if w.file == nil {
		return 0, fmt.Errorf("rollog: 日志文件丢失且重建失败")
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}

	if w.size >= w.cfg.MaxSizeBytes {
		// 轮转失败只上报，不改变本次写入的结果。
		if rotErr := w.rotateLocked(); rotErr != nil {
			w.cfg.OnError(fmt.Errorf("rollog: 轮转失败（继续写入当前文件）: %w", rotErr))
		}
	}
	return n, nil
}

// reopenIfVanishedLocked 检测活跃文件是否已被外部删除，若是则重新创建。
//
// # 为什么必须做这件事
//
// 进程持有的是**打开的文件描述符**。若有人（运维清理、误删、或外部 logrotate 的非
// copytruncate 策略）把路径上的文件删掉，写操作**不会报错**——只会写进一个已无目录项的
// inode，重新打开路径得到的是空文件。结果是：面板与 grep 都再也看不到新日志，
// 而进程完全不报错。
//
// 2026-10-29 真机验证时实际踩到：删掉 control-plane.log 后 `ls` 显示目录为空、
// `ls -l /proc/<pid>/fd` 显示 `(deleted)`，而所有日志调用一路“成功”。
//
// # 为什么不是每次 Write 都 stat
//
// `os.Stat` 是系统调用，而日志写入是高频路径。这里按时间窗口采样（默认 1s），
// 兼顾“尽快自愈”与“不把 stat 变成热点”。检测失败不阻断写入。
func (w *Writer) reopenIfVanishedLocked() {
	now := time.Now()
	if now.Sub(w.lastStat) < w.statInterval {
		return
	}
	w.lastStat = now

	// 目录项还在就认为正常。**不比较大小**：外部 `>>` 追加会让大小对不上，
	// 那不是“丢失”，重建反而会把外部写入的内容变成无人可见的旁支。
	info, err := os.Stat(w.cfg.Path)
	if err == nil && info != nil {
		return
	}
	if err != nil && !os.IsNotExist(err) {
		// 权限/IO 错误：不动，交给 Write 报错。
		return
	}

	// 文件确实不在了：重建它，把 fd 换到新文件上。
	// 旧 fd 不必显式关闭——它指向已删除的 inode，关闭即释放空间。
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	if reopenErr := w.openActiveLocked(); reopenErr != nil {
		w.cfg.OnError(fmt.Errorf("rollog: 活跃日志文件被外部删除后重建失败: %w", reopenErr))
		return
	}
	w.cfg.OnError(fmt.Errorf("rollog: 检测到 %s 被外部删除，已重建（此前一段日志写在了已删除的 inode 上）", filepath.Base(w.cfg.Path)))
}

// Rotate 立即触发一次轮转（供 SIGHUP 或管理面手动触发）。
func (w *Writer) Rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	if w.size == 0 {
		// 空文件不轮转：避免产生一堆 0 字节归档（与 logrotate 的 notifempty 同口径）。
		return nil
	}
	return w.rotateLocked()
}

// Size 返回当前活跃文件的字节数。
func (w *Writer) Size() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.size
}

// Close 刷新并关闭 Writer，等待后台压缩任务收尾。可重复调用。
func (w *Writer) Close() error {
	var err error
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		if w.file != nil {
			// 关闭前不强制 fsync：进程正常退出时内核会落盘，
			// 而每条日志 fsync 会让日志路径变成性能瓶颈（日志本就允许丢最后一小段）。
			err = w.file.Close()
			w.file = nil
		}
		w.mu.Unlock()

		if w.compressQ != nil {
			close(w.compressQ)
		}
		close(w.done)
		w.wg.Wait()
	})
	return err
}

// openActiveLocked 打开（或创建）活跃文件并初始化 size。调用方须持有锁（Open 早期除外）。
func (w *Writer) openActiveLocked() error {
	f, err := os.OpenFile(w.cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("rollog: 打开日志文件失败: %w", err)
	}
	info, statErr := f.Stat()
	if statErr != nil {
		_ = f.Close()
		return fmt.Errorf("rollog: 读取日志文件状态失败: %w", statErr)
	}
	w.file = f
	w.size = info.Size()
	return nil
}

// rotateLocked 执行一次轮转。调用方须持有锁。
//
// 顺序是「先 fsync → 再 close → 再 rename → 再新建」：
//   - 先 fsync 是为了让 rename 之前的字节真的落盘（轮转是低频事件，这次 fsync 值得）；
//   - 先 close 再 rename 是因为 Windows 上重命名已打开的文件会失败，Linux 上则两者皆可，
//     故取可移植的那一侧；
//   - 整个序列在锁内完成，其他写者会短暂等待——但这段里**没有字节复制**，只有一次 rename。
func (w *Writer) rotateLocked() error {
	if w.file == nil {
		return fmt.Errorf("rollog: 日志文件未打开")
	}
	if err := w.file.Sync(); err != nil {
		// fsync 失败不阻断轮转：继续 rename，最多丢最后一小段。
		w.cfg.OnError(fmt.Errorf("rollog: 轮转前 fsync 失败（继续）: %w", err))
	}
	if err := w.file.Close(); err != nil {
		w.cfg.OnError(fmt.Errorf("rollog: 关闭待轮转文件失败（继续）: %w", err))
	}
	w.file = nil

	target, err := w.nextRotatedName()
	if err != nil {
		// rename 名都没算出来：尽力恢复活跃文件，让写入继续。
		if reopenErr := w.openActiveLocked(); reopenErr != nil {
			return fmt.Errorf("轮转命名失败且重开失败: %v / %w", err, reopenErr)
		}
		return err
	}

	if err := os.Rename(w.cfg.Path, target); err != nil {
		if reopenErr := w.openActiveLocked(); reopenErr != nil {
			return fmt.Errorf("轮转重命名失败且重开失败: %v / %w", err, reopenErr)
		}
		return fmt.Errorf("重命名 %s → %s 失败: %w", filepath.Base(w.cfg.Path), filepath.Base(target), err)
	}

	if err := w.openActiveLocked(); err != nil {
		// 活跃文件打不开，但归档已生成：把错误抛给调用方，写路径会在下次 Write 时报未打开。
		return fmt.Errorf("轮转后新建活跃文件失败: %w", err)
	}

	w.enqueueCompress(target)
	return nil
}

// nextRotatedName 生成轮转目标名，同秒冲突时追加序号。
func (w *Writer) nextRotatedName() (string, error) {
	ts := time.Now().Format("20060102-150405")
	for i := 0; i < 100; i++ {
		name := filepath.Join(w.dir, fmt.Sprintf("%s-%s%s", w.base, ts, w.ext))
		if i > 0 {
			name = filepath.Join(w.dir, fmt.Sprintf("%s-%s-%d%s", w.base, ts, i, w.ext))
		}
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return name, nil
		}
	}
	return "", fmt.Errorf("同一秒内轮转次数过多")
}

// enqueueCompress 把轮转档交给后台压缩。队列满时放弃（留未压缩文件），不阻塞。
func (w *Writer) enqueueCompress(path string) {
	if w.compressQ == nil {
		return
	}
	select {
	case w.compressQ <- path:
	default:
		// 有意丢弃：轮转不能因为压缩排队而阻塞。未压缩的轮转档会在
		// 下次 Open 的补压扫描中被处理。
		w.cfg.OnError(fmt.Errorf("rollog: 压缩队列已满，暂不压缩 %s（下次启动补压）", filepath.Base(path)))
	}
}

// enqueueExistingRotated 扫描上次运行遗留的未压缩轮转档并入队。
func (w *Writer) enqueueExistingRotated() {
	paths, err := w.listRotated(false)
	if err != nil {
		w.cfg.OnError(fmt.Errorf("rollog: 扫描遗留轮转档失败: %w", err))
		return
	}
	for _, p := range paths {
		w.enqueueCompress(p)
	}
}

// listRotated 返回轮转档路径（按文件名降序，即最新在前）。
// onlyCompressed=true 只返回 .gz，false 只返回未压缩。
func (w *Writer) listRotated(onlyCompressed bool) ([]string, error) {
	var out []string

	pattern := filepath.Join(w.dir, w.base+"-*"+w.ext)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	for _, m := range matches {
		if !onlyCompressed {
			out = append(out, m)
		}
	}

	if onlyCompressed {
		gzMatches, err := filepath.Glob(pattern + ".gz")
		if err != nil {
			return nil, err
		}
		out = append(out, gzMatches...)
	}

	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// applyRetention 删除超过 MaxBackups 的最旧轮转档（未压缩与已压缩一起计数）。
func (w *Writer) applyRetention() error {
	if w.cfg.MaxBackups < 0 {
		return nil
	}
	plain, err := w.listRotated(false)
	if err != nil {
		return err
	}
	compressed, err := w.listRotated(true)
	if err != nil {
		return err
	}

	all := make([]string, 0, len(plain)+len(compressed))
	all = append(all, plain...)
	all = append(all, compressed...)
	// 再统一排序：未压缩与已压缩混在一起按名字（含时间戳）降序。
	sort.Sort(sort.Reverse(sort.StringSlice(all)))

	for i := w.cfg.MaxBackups; i < len(all); i++ {
		if err := os.Remove(all[i]); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除过期轮转档 %s 失败: %w", filepath.Base(all[i]), err)
		}
	}
	return nil
}
