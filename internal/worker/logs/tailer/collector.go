package tailer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Line 是一条待输出的日志行。
type Line struct {
	// Path 是它来自哪个文件（用于推导源标签与排障）。
	Path string
	// Content 是行内容（不含换行符）。
	Content []byte
	// Identity 是文件身份（供上层区分“同一源的不同文件”）。
	Identity FileIdentity
}

// Sink 是采集器的输出端。
//
// # 职责边界
//
// 采集器只负责“把文件里的新行抽出来交给 Sink”，不关心它们怎么变成事件、
// 怎么打标签、怎么写 VL。这与 Fluent Bit 的“input 只负责把数据塞进引擎”同构。
//
// # 为什么返回 error
//
// Sink 失败（如 VL 不可用）时应把错误传回采集器，让采集器暂停推进 offset——
// 否则数据就真的丢了。这是“至少不丢”的最小保证。
type Sink interface {
	Consume(ctx context.Context, lines []Line) error
}

// SinkFunc 让普通函数可作为 Sink。
type SinkFunc func(ctx context.Context, lines []Line) error

func (f SinkFunc) Consume(ctx context.Context, lines []Line) error { return f(ctx, lines) }

// Collector 把 scanner / harvester / registry 三层组装起来（对齐 Filebeat 的 input 实例）。
//
// # 循环结构
//
//	每 ScanInterval：扫目录 → 发现新文件 → 建 harvester
//	每 ProgressInterval：轮询所有 harvester → 读新行 → 交 Sink → 更新 registry
//	每 FlushInterval：registry 落盘
//
// 两个间隔分开的理由（与官方一致）：扫描目录是重操作（Glob + Stat + 指纹），
// 而读文件是轻操作（只是 Seek + Read）。
type Collector struct {
	cfg  CollectorConfig
	reg  *Registry
	sink Sink

	mu         sync.Mutex
	harvesters map[string]*Harvester // key: Identity.Key()

	// batchSize 是单次交给 Sink 的最大行数。
	batchSize int
	// onGap 在“有文件未读完就消失”时被调用（对齐用户要求：丢得明白）。
	onGap func(path string, unreadBytes int64)
	// harvestable 决定一个**新发现**的文件是否值得建 harvester（nil = 全部）。
	//
	// # 为什么需要它（丢数据的硬防护）
	//
	// 上层靠 resolver 把路径映成源标签，映射不上就跳过该行——但 offset 照旧推进。
	// 于是在「绑定表还没就绪」的窗口里（首次部署、CP 重启后尚未下发登记），
	// ReadFromHead 会把整个历史文件读一遍后逐行丢弃，而位点已经推到文件末尾——
	// 那段历史**永久不采了**，且日志里只有一条“无法识别源”的计数。
	//
	// 装配层传入判别函数后，这类文件根本不建 harvester，位点不动，等绑定到达后
	// 再从头采——这是同一件事的正确方向：**宁可晚采，不可静默丢**。
	harvestable func(path string) bool
	// warnedUnready 记录已告警过的「暂不可采」路径（避免每轮扫描刷屏）。
	warnedUnready map[string]struct{}
}

// NewCollector 创建一个采集器。
//
// reg 可为 nil（纯内存模式，用于测试）。
func NewCollector(cfg CollectorConfig, reg *Registry, sink Sink) *Collector {
	cfg = cfg.withDefaults()
	if reg == nil {
		reg = &Registry{m: make(map[string]FileState)}
	}
	return &Collector{
		cfg:           cfg,
		reg:           reg,
		sink:          sink,
		harvesters:    make(map[string]*Harvester),
		batchSize:     500,
		warnedUnready: make(map[string]struct{}),
	}
}

// SetHarvestable 注入「新发现的文件是否值得采」的判据（nil = 全部采）。
//
// 仅影响**新建 harvester**；已在采的文件不受后加的判据影响（否则中途改判据会
// 把在跑的文件当成“消失”而误报 gap）。
func (c *Collector) SetHarvestable(fn func(path string) bool) {
	c.harvestable = fn
}

// SetGapReporter 注入“文件未读完就消失”的回调。
//
// 用户要求：“接受丢失，但记录下来”——这是记录的那个口子。
func (c *Collector) SetGapReporter(fn func(path string, unreadBytes int64)) {
	c.onGap = fn
}

// Run 启动采集循环，阻塞直到 ctx 取消。
func (c *Collector) Run(ctx context.Context) error {
	scanTicker := time.NewTicker(c.cfg.ScanInterval)
	defer scanTicker.Stop()
	readTicker := time.NewTicker(c.cfg.ProgressInterval)
	defer readTicker.Stop()

	// 立即先扫一次：不等第一个 tick，否则启动后要等 10s 才采到东西。
	if err := c.scanOnce(ctx); err != nil {
		slog.Warn("tailer: 首次扫描失败", "error", err)
	}
	if err := c.readOnce(ctx); err != nil {
		slog.Warn("tailer: 首次读取失败", "error", err)
	}
	if err := c.reg.Flush(); err != nil {
		slog.Warn("tailer: 位点落盘失败", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			c.shutdown()
			return ctx.Err()
		case <-scanTicker.C:
			if err := c.scanOnce(ctx); err != nil {
				slog.Warn("tailer: 扫描失败", "error", err)
			}
		case <-readTicker.C:
			if err := c.readOnce(ctx); err != nil {
				slog.Warn("tailer: 读取失败", "error", err)
			}
			// 每次读完就落盘：与 Fluent Bit `db.sync=normal` 同量级。
			// 代价是每 ProgressInterval（2s）一次小文件写，量级几 KB。
			if err := c.reg.Flush(); err != nil {
				slog.Warn("tailer: 位点落盘失败", "error", err)
			}
		}
	}
}

// scanOnce 扫描一次目录，为新文件建 harvester。
func (c *Collector) scanOnce(ctx context.Context) error {
	files, err := c.cfg.Scan()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[string]struct{}, len(files))
	for _, f := range files {
		key := f.Identity.Key()
		seen[key] = struct{}{}
		if _, exists := c.harvesters[key]; exists {
			// 已在该文件：只更新路径（轮转后路径会变，fingerprint 不变）。
			c.harvesters[key].SetPath(f.Path)
			continue
		}
		// 新文件：决定起始位置。
		if c.harvestable != nil && !c.harvestable(f.Path) {
			// 暂不可采（如绑定表里还没有这个目录的归属）：**不建 harvester、不碰位点**。
			// 详见 Collector.harvestable 的注释：先读后丢是不可恢复的丢数据。
			if _, warned := c.warnedUnready[f.Path]; !warned {
				c.warnedUnready[f.Path] = struct{}{}
				slog.Warn("tailer: 文件暂不可采（尚未识别所属源），位点不推进，等绑定到达后再采",
					"path", f.Path)
			}
			continue
		}
		startAt := c.startOffsetFor(f)
		h, hErr := NewHarvester(f, startAt, 0)
		if hErr != nil {
			// 打开失败（权限/竞态删除）不应拖垮整体。
			slog.Warn("tailer: 打开文件失败，跳过", "path", f.Path, "error", hErr)
			continue
		}
		c.harvesters[key] = h
		// 立即登记位点：下次重启就能从这开始。
		c.reg.Put(FileState{
			Identity: f.Identity,
			Path:     f.Path,
			Offset:   h.Offset(),
			Size:     f.Size,
		})
	}

	// 处理消失的 harvester：文件可能已被压缩/删除。
	for key, h := range c.harvesters {
		if _, still := seen[key]; still {
			continue
		}
		// 文件已不在扫描结果里。先把残留行冲掉，再判定是否真的丢失。
		c.flushHarvester(ctx, h)
		if h.PendingLength() > 0 || c.unread(h) > 0 {
			// 有未读完的字节 ⇒ 上报丢失（用户要求“丢得明白”）。
			if c.onGap != nil {
				c.onGap(h.Path(), c.unread(h)+int64(h.PendingLength()))
			}
			slog.Warn("tailer: 文件在读完前消失（可能被压缩为 .gz）",
				"path", h.Path(), "offset", h.Offset(), "pending", h.PendingLength())
		}
		_ = h.Close()
		delete(c.harvesters, key)
		// 保留 registry 条目而不是删除：
		// 否则下次同名文件出现会被当成“从未读过”，从 0 重读产生大量重复。
		// 这与 Filebeat `clean_removed` **默认关闭** 的取舍一致。
	}
	return nil
}

// startOffsetFor 决定新发现的文件从哪里开始读。
func (c *Collector) startOffsetFor(f DiscoveredFile) int64 {
	// 已有位点：从上次位置继续（含小文件增长的前缀匹配，见 Registry.GetGrowing）。
	if st, ok := c.reg.GetGrowing(f.Identity, f.Path, f.Size); ok {
		// 若文件变小了（截断/换文件），NewHarvester 会自己校正到 0。
		return st.Offset
	}
	if c.cfg.ReadFromHead {
		return 0
	}
	// 经典 tail 行为：只采新内容。
	return f.Size
}

// unread 返回文件还有多少字节未读。
func (c *Collector) unread(h *Harvester) int64 {
	size, _, err := h.Stat()
	if err != nil {
		return 0
	}
	if size <= h.Offset() {
		return 0
	}
	return size - h.Offset()
}

// readOnce 轮询所有 harvester 读新内容。
func (c *Collector) readOnce(ctx context.Context) error {
	c.mu.Lock()
	hs := make([]*Harvester, 0, len(c.harvesters))
	for _, h := range c.harvesters {
		hs = append(hs, h)
	}
	c.mu.Unlock()

	var batch []Line
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := c.sink.Consume(ctx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for _, h := range hs {
		// 截断检测：文件被清空重写时 offset 失效，必须重置。
		if truncated, err := h.DetectTruncation(); err == nil && truncated {
			slog.Warn("tailer: 检测到文件被截断，从头重读", "path", h.Path())
		}
		identity := h.Identity()
		path := h.Path()
		_, readErr := h.ReadLines(func(line []byte) error {
			batch = append(batch, Line{Path: path, Content: line, Identity: identity})
			if len(batch) >= c.batchSize {
				return flush()
			}
			return nil
		})
		if readErr != nil {
			return fmt.Errorf("tailer: 读 %s: %w", path, readErr)
		}
		// 每读完一个文件就更新它的位点。
		//
		// Size 必须写**算指纹时**的大小（IdentitySize），而不是当前大小：
		// 它是 FingerprintPrefixLen 反推前缀长度的依据，写错会让增长判定永远失败。
		c.reg.Put(FileState{
			Identity:       identity,
			Path:           path,
			Offset:         h.Offset(),
			Size:           h.IdentitySize(),
			LastActiveUnix: time.Now().Unix(),
		})
	}
	return flush()
}

// flushHarvester 把某个 harvester 的残留半行交出去。
func (c *Collector) flushHarvester(ctx context.Context, h *Harvester) {
	identity := h.Identity()
	path := h.Path()
	var batch []Line
	_ = h.FlushPending(func(line []byte) error {
		batch = append(batch, Line{Path: path, Content: line, Identity: identity})
		return nil
	})
	if len(batch) > 0 {
		if err := c.sink.Consume(ctx, batch); err != nil {
			slog.Warn("tailer: 冲刷残留行失败", "path", path, "error", err)
		}
	}
}

// shutdown 停止时冲掉所有残留并落盘。
func (c *Collector) shutdown() {
	ctx := context.Background()
	c.mu.Lock()
	hs := make([]*Harvester, 0, len(c.harvesters))
	for _, h := range c.harvesters {
		hs = append(hs, h)
	}
	c.mu.Unlock()
	for _, h := range hs {
		c.flushHarvester(ctx, h)
		_ = h.Close()
	}
	if err := c.reg.Flush(); err != nil {
		slog.Warn("tailer: 关停时位点落盘失败", "error", err)
	}
}

// Harvesters 返回当前活跃的 harvester 数量（供观测）。
func (c *Collector) Harvesters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.harvesters)
}
