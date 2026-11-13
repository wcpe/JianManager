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
	"strconv"
	"strings"
	"sync"
	"time"
)

// 默认参数。调用方通常只覆盖 Path。
const (
	// DefaultMaxSizeBytes 单个活跃文件触发轮转的大小（1 GiB）。
	// 轮转只有这一处实现（进程内 rename），没有需要与之对齐的外部阈值。
	DefaultMaxSizeBytes int64 = 1 << 30
	// DefaultMaxBackups 轮转档保留数量（含未压缩与已压缩）。
	DefaultMaxBackups = 5
	// DefaultMaxAgeDays 轮转档保留天数。按文件名里的轮转时间计，一天 = 24h。
	// 与 DefaultMaxBackups 是两个独立上限，谁更严格谁生效（同 lumberjack 语义）。
	//
	// 为什么要有它：MaxBackups 数的是「份数」，而份数在不同负载下代表的时长差很多——
	// 事故期一小时转 5 次就只剩 1 小时历史，平常一周不轮转就堆着几个月的日志。
	// 天数口径直接对齐运维的心智（「保留 30 天」）。
	DefaultMaxAgeDays = 30
	// maintQueueSize 后台维护（压缩 + 保留清理）队列容量。轮转是低频事件，64 足够；
	// 队列满时宁可放弃本次维护（下次启动会补做）也不阻塞写路径。
	maintQueueSize = 64
	// defaultStatInterval 是“活跃文件是否被外部删除”的检测窗口。
	// 1s 是两方面的折中：偏长会让自愈变慢（期间日志写进已删除 inode），
	// 偏短会把 os.Stat 变成高频系统调用。日志是秒级可接受的路径。
	defaultStatInterval = time.Second
)

// Retention 是应用层（CP / Worker 各自的 LogConfig）转过来的保留策略。
//
// # 为何不直接让应用传 Config
//
// Config 里还有 Path / OnError 这类进程相关项，而「留多少、存多久、压不压」三项
// 是**同一个运维语义**，两个应用应完全一致。把转换放在本包里，
// 两边就不会因为“一个传了 max_age、另一个忘了”而双轨。
type Retention struct {
	MaxSizeBytes int64
	MaxBackups   int
	MaxAgeDays   int
	Compress     bool
}

// DefaultRetention 返回内置默认（1 GiB / 5 份 / 30 天 / 开压缩）。
func DefaultRetention() Retention {
	return Retention{
		MaxSizeBytes: DefaultMaxSizeBytes,
		MaxBackups:   DefaultMaxBackups,
		MaxAgeDays:   DefaultMaxAgeDays,
		Compress:     true,
	}
}

// Config 是 Writer 的配置。
type Config struct {
	// Path 是活跃日志文件的完整路径（如 /opt/jm/data/log/worker.log）。
	// 父目录不存在时会自动创建。
	Path string
	// MaxSizeBytes 单文件触发轮转的字节数。<=0 表示用 DefaultMaxSizeBytes。
	MaxSizeBytes int64
	// MaxBackups 保留的轮转档数量（未压缩与已压缩一起计数）。0 表示用 DefaultMaxBackups；
	// 负值表示不限（不推荐，会导致磁盘无限增长）。
	MaxBackups int
	// MaxAgeDays 轮转档保留天数，超过该天数的档会被删除（与 MaxBackups 各自独立生效，
	// 任一条件命中即删）。0 表示用 DefaultMaxAgeDays；**负值表示关闭按天保留**。
	//
	// 时间取自文件名里的轮转时间戳（不是 mtime）：mtime 会被解压、备份、rsync 等
	// 外部动作改掉，一旦被改晚，一份早该删的日志就能无限留存。
	MaxAgeDays int
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

	// maintQ 后台维护队列（压缩 + 保留清理）。
	//
	// # 为什么把两件事放进同一条队列
	//
	// 曾经只用一条「待压缩路径」队列，并在压缩协程里顺带做保留清理。代价是：
	// DisableCompress 时协程根本不启动 ⇒ **MaxBackups 完全不生效**（保留 N 份的承诺
	// 静默作废）。保留清理与压缩是否开启无关，它必须有自己的驱动来源。
	maintQ    chan maintTask
	closeOnce sync.Once
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
	if cfg.MaxAgeDays == 0 {
		cfg.MaxAgeDays = DefaultMaxAgeDays
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
		maintQ:       make(chan maintTask, maintQueueSize),
		statInterval: defaultStatInterval,
	}

	if err := w.openActiveLocked(); err != nil {
		return nil, err
	}

	// 维护协程总是启动：即使禁用压缩，保留清理仍必须生效（见 Writer.maintQ 的说明）。
	w.wg.Add(1)
	go w.maintenanceLoop()

	// 启动时补做上次运行的收尾：
	//   - 进程可能在 rename 之后、压缩之前退出，留下未压缩的轮转档；
	//   - 退出期间可能已跨过 MaxAgeDays（停机数天后重启，旧档该清而无人清）。
	// 这两件事都归第一次维护任务处理。
	w.scheduleMaint(maintTask{compressPaths: w.pendingCompressPaths()})

	return w, nil
}

// Path 返回活跃日志文件的路径。诊断接口据此定位文件。
func (w *Writer) Path() string { return w.cfg.Path }

// Write 实现 io.Writer。
//
// 写入成功后若跨过大小阈值，立即在锁内完成轮转（只有 rename，O(1)）。
// 轮转失败不影响已经成功的那次写入的返回结果——日志宁可继续写进大文件，
// 也不能因为轮转失败而让调用方以为写失败了。
//
// # 失败必须经 OnError 上报（2026-11-09）
//
// 本函数的两个失败分支（已关闭 / fd 重建失败）原本只 `return 0, err`，不经 OnError。
// 而调用方是 slog：标准库 logger.go 里是 `_ = l.Handler().Handle(...)`，**显式丢弃**
// Handler 返回的错误（io.MultiWriter 也只透传）。于是这条链路是：
// 日志写不进去 → 没有文件内容 → OnError 没被调 → stderr 也没输出。
// 运维看到的是“日志突然停在某一行以后没了”，无任何入口可查。
// 这正是本包注释开篇批判的“进程完全不报错”那一类事故。
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		// 关闭后仍被写（典型：退出流程中残留的 goroutine 还在打日志）。
		// 不刷 stderr：退出阶段每个协程都报一遍会变成日志风暴。
		return 0, os.ErrClosed
	}

	// 自愈：文件可能被外部删除（见 reopenIfVanishedLocked 的说明）。
	w.reopenIfVanishedLocked()
	if w.file == nil {
		// 上一次重建失败（ENOSPC/权限）。正常路径里 statInterval 会把重建频率降到 1s 一次，
		// 但 w.file == nil 意味着**当前每次写都在丢日志**，此时等那 1s 窗口是纯损失。
		// 代价只有一次 OpenFile，远小于静默丢一行日志。
		if reopenErr := w.openActiveLocked(); reopenErr == nil {
			w.cfg.OnError(fmt.Errorf("rollog: 日志文件不可写后已重新打开 %s（此前一段日志丢失）", filepath.Base(w.cfg.Path)))
		} else {
			err := fmt.Errorf("rollog: 日志文件丢失且重建失败，本次写入丢弃: %w", reopenErr)
			w.cfg.OnError(err)
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		// 磁盘满、fd 失效等：slog 会吞掉这个 error，所以必须在这里上报。
		w.cfg.OnError(fmt.Errorf("rollog: 写入 %s 失败: %w", filepath.Base(w.cfg.Path), err))
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

// Close 刷新并关闭 Writer，等待后台维护（压缩 + 保留清理）收尾。可重复调用。
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

		// 关闭队列让维护协程把积压任务做完再退出（range 语义），
		// 这样正常退出时不会留下未压缩的轮转档。
		close(w.maintQ)
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

	w.enqueueMaint(target)
	return nil
}

// rotatedTimeLayout 是轮转档名里的时间戳格式（与 nextRotatedName 一致）。
const rotatedTimeLayout = "20060102-150405"

// nextRotatedName 生成轮转目标名，同秒冲突时追加序号。
//
// # 为什么必须同时检查 .log 与 .log.gz
//
// 轮转档会经异步压缩从 `<ts>.log` 变成 `<ts>.log.gz`。若只检查 `.log`，
// 同一秒内就会发生这个序列：
//
//	t0  轮转 → 得到 <ts>.log
//	t1  压缩完成 → <ts>.log 消失，只剩 <ts>.log.gz
//	t2  同秒内再次轮转 → 检查 <ts>.log 不存在 ⇒ **复用同名** → 两份不同数据撞车
//
// 后果不只是“名字难看”：parseRotatedName 对两者都解出 (when, seq=0)，
// 保留策略无法区分先后，可能删错其中一份。
// 这是 2026-11-08 真机并发压测（8 协程 / 200KB 阈值）直接暴露出来的缺陷，
// 单元测试因为写入速率较慢、没压出同秒多次轮转而没能发现。
func (w *Writer) nextRotatedName() (string, error) {
	ts := time.Now().Format(rotatedTimeLayout)
	for i := 0; i < 100; i++ {
		name := filepath.Join(w.dir, fmt.Sprintf("%s-%s%s", w.base, ts, w.ext))
		if i > 0 {
			name = filepath.Join(w.dir, fmt.Sprintf("%s-%s-%d%s", w.base, ts, i, w.ext))
		}
		if !w.rotatedNameTaken(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("同一秒内轮转次数过多")
}

// rotatedNameTaken 检查某个候选轮转名是否已被占用。
//
// 同时看三种形态：未压缩的 .log、已发布的 .gz、以及压缩中的 .gz.tmp。
// 最后一种是必要的：压缩还没完成时 .gz 尚不存在，但 .tmp 已经占着这个名字了。
func (w *Writer) rotatedNameTaken(name string) bool {
	for _, p := range []string{name, name + compressSuffix, name + compressSuffix + tmpSuffix} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// enqueueMaint 投递一次后台维护任务（压缩刚轮转出的档 + 统一做保留清理）。
//
// 队列满时**只放弃压缩**，保留清理仍必须执行——所以打印告警时不能把整件事
// 说成“跳过维护”（那会让人以为磁盘不会收敛）。清理与压缩走同一个任务的另一个字段。
func (w *Writer) enqueueMaint(rotatedPath string) {
	task := maintTask{}
	if !w.cfg.DisableCompress {
		task.compressPaths = []string{rotatedPath}
	}
	if !w.scheduleMaint(task) && !w.cfg.DisableCompress {
		w.cfg.OnError(fmt.Errorf("rollog: 维护队列已满，暂不压缩 %s（下次启动补压）", filepath.Base(rotatedPath)))
	}
}

// scheduleMaint 尝试非阻塞地投递一次维护任务，返回是否投递成功。
//
// 队列满时投递失败是**可接受**的：压缩会在下次 Open 的补压扫描里补上，
// 而保留清理在下一次轮转时再做。写路径绝不能因此阻塞。
func (w *Writer) scheduleMaint(task maintTask) bool {
	select {
	case w.maintQ <- task:
		return true
	default:
		return false
	}
}

// pendingCompressPaths 返回当前尚未压缩的轮转档（不含 .gz）。
func (w *Writer) pendingCompressPaths() []string {
	if w.cfg.DisableCompress {
		return nil
	}
	files, err := w.listRotated()
	if err != nil {
		w.cfg.OnError(fmt.Errorf("rollog: 扫描遗留轮转档失败: %w", err))
		return nil
	}
	var out []string
	for _, f := range files {
		if !strings.HasSuffix(f.path, ".gz") {
			out = append(out, f.path)
		}
	}
	return out
}

// listRotated 返回轮转档路径，**按轮转时间递增**（最旧在前）。
//
// 排序按从文件名里解析出的时间戳与同秒序号进行，而**不是**按字符串排序。
//
// # 为什么不能按字符串排
//
// 同秒多次轮转的档名形如 `app-20260101-120000.log` 与 `app-20260101-120000-1.log`。
// 字符串升序里 `-`(0x2D) < `.`(0x2E)，于是**无序号的那个排在前面**——即把后
// 轮转出来的档当成了更早的。保留策略因此会删错文件（删掉较新的，留下更早的）。
func (w *Writer) listRotated() ([]rotatedFile, error) {
	plain, err := filepath.Glob(filepath.Join(w.dir, w.base+"-*"+w.ext))
	if err != nil {
		return nil, err
	}
	gz, err := filepath.Glob(filepath.Join(w.dir, w.base+"-*"+w.ext+".gz"))
	if err != nil {
		return nil, err
	}

	out := make([]rotatedFile, 0, len(plain)+len(gz))
	for _, p := range append(plain, gz...) {
		rf, ok := w.parseRotatedName(p)
		if !ok {
			// 名字不由本包生成（人工拷贝进来的、或换个名字的日志）：
			// **不纳入管理**，既不压缩也不删。"不删自己认不得的文件" 比 "尽量清干净" 更重要。
			continue
		}
		out = append(out, rf)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].when.Equal(out[j].when) {
			return out[i].seq < out[j].seq
		}
		return out[i].when.Before(out[j].when)
	})
	return out, nil
}

// rotatedFile 是一个被本包认可的轮转档。
type rotatedFile struct {
	path string
	when time.Time // 轮转时间（取自文件名）
	seq  int       // 同秒序号，无序号为 0
}

// parseRotatedName 从文件名解析轮转时间与同秒序号。
// 不匹配本包命名规则时返回 ok=false。
func (w *Writer) parseRotatedName(path string) (rotatedFile, bool) {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, ".gz")
	name = strings.TrimSuffix(name, w.ext)

	prefix := w.base + "-"
	if !strings.HasPrefix(name, prefix) {
		return rotatedFile{}, false
	}
	rest := name[len(prefix):]

	// 先试“无序号”形态：时间戳本身含 `-`（20260101-120000），
	// 若先拆序号会把时间的前半段当成序号吃掉（20260101 | 120000），
	// 整体解析必然失败且匹配不到任何档——**顺序错了就全盘失效**。
	if when, err := time.ParseInLocation(rotatedTimeLayout, rest, time.Local); err == nil {
		return rotatedFile{path: path, when: when, seq: 0}, true
	}

	// 再试“<ts>-<seq>”形态（同秒多次轮转）。
	if dash := strings.LastIndex(rest, "-"); dash > 0 {
		if n, err := strconv.Atoi(rest[dash+1:]); err == nil {
			if when, perr := time.ParseInLocation(rotatedTimeLayout, rest[:dash], time.Local); perr == nil {
				return rotatedFile{path: path, when: when, seq: n}, true
			}
		}
	}
	return rotatedFile{}, false
}

// applyRetention 删除过期轮转档。
//
// 两条独立上限，任一命中即删：
//   - MaxBackups：保留份数（负值 = 不限）；
//   - MaxAgeDays：保留天数（负值 = 关闭）。
//
// 保留清理与压不压缩**无关**，必须无条件生效（见 Writer.maintQ 的说明）。
func (w *Writer) applyRetention() error {
	files, err := w.listRotated()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	// 待删集合：最新的排在前（files 是递增序，从尾部开始数）。
	doomed := make([]bool, len(files))

	if w.cfg.MaxBackups >= 0 {
		// 保留最新的 MaxBackups 份；更早的全部删除。
		keep := w.cfg.MaxBackups
		if keep < 0 {
			keep = 0
		}
		for i := 0; i < len(files)-keep; i++ {
			doomed[i] = true
		}
	}

	if w.cfg.MaxAgeDays > 0 {
		// 一天 = 24h（与 lumberjack 同口径）；用档名里的轮转时间，不用 mtime。
		cutoff := time.Now().Add(-time.Duration(w.cfg.MaxAgeDays) * 24 * time.Hour)
		for i, f := range files {
			if f.when.Before(cutoff) {
				doomed[i] = true
			}
		}
	}

	var firstErr error
	for i, f := range files {
		if !doomed[i] {
			continue
		}
		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			if firstErr == nil {
				firstErr = fmt.Errorf("删除过期轮转档 %s 失败: %w", filepath.Base(f.path), err)
			}
		}
	}
	return firstErr
}
