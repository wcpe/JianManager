package pipeline

import (
	"fmt"
	"io"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// Source 是采集来源的统一抽象（FR-512）。
//
// # 为什么现在才抽
//
// 在 2026-10-31 之前，三种模式（FILE_PRIMARY / STDIO_PRIMARY / PUSH_ONLY）的**下半身**
// 其实已经完全统一了：一旦事件进入 `Pipeline.Ingest`，走的就是同一条
// `AppendEvents → CommitAndQualify → DeliverQualified → TryReclaim`，
// 共享同一个 WAL 上限、同一套攒批/压缩/流标签/背压/丢弃语义。
//
// 真正有差异的只有**上半身**——「事件从哪来」与「回收水位怎么起步」。
// 本接口就是把这两个差异点显式化，使新增一种来源（journald、syslog、Kafka…）
// 只需实现本接口，而不必再改 Pipeline 的构造分支与 pollLocked 的 switch。
//
// # 为什么要拆成两个接口
//
// Poller 与 SegmentRegistrar 的**生命周期不同**：
//   - Poller 在采集轮里被反复调用（热路径，持 p.mu）；
//   - SegmentRegistrar 只在建管道时调一次（冷路径，用来给账本一个归属）。
//
// 把它们捆成一个接口会让「只需要登记、不需要轮询」的 PUSH_ONLY 被迫实现一个
// 永远返回 nil 的 Poll；那看起来很无害，但它是**假实现**——读者会以为它在做事。
// 拆开后 PUSH_ONLY 显式不实现 Poller，语义就直白了。
type Source interface {
	// Mode 返回采集模式标识（写进账本与观测面）。
	Mode() AcquireMode
	// Register 在建管道时登记分段归属（给账本一个回收/对账的起点）。
	Register(led *ledger.Ledger, key ledger.SourceKey, src logtypes.SourceIdentity) error
}

// Poller 是**主动拉取型**来源额外实现的接口。
//
// 推送型来源（PUSH_ONLY）不实现它：事件由外部经 Pipeline.Ingest 注入，
// 主动 Poll 永远无新事件。这不是遗漏，是设计。
type Poller interface {
	Source
	// Poll 拉取一批事件。调用方持 p.mu。
	Poll() ([]logtypes.Event, error)
}

// fileSource FILE_PRIMARY：tail 活文件（可带轮转目标与起始位置）。
type fileSource struct {
	p *Pipeline
}

func (s fileSource) Mode() AcquireMode { return ModeFilePrimary }

func (s fileSource) Register(led *ledger.Ledger, key ledger.SourceKey, src logtypes.SourceIdentity) error {
	// 文件源的分段由 FileTailer 在构造时自己登记（它知道 live 段的 Path/StartPos/身份），
	// 这里不再重复登记——重复登记会让账本里出现两条归属不同的段。
	_ = led
	_ = key
	_ = src
	return nil
}

func (s fileSource) Poll() ([]logtypes.Event, error) { return s.p.pollFile() }

// stdioSource STDIO_PRIMARY：受管 Raw 分段或内存 Reader。
type stdioSource struct {
	p *Pipeline
}

func (s stdioSource) Mode() AcquireMode { return ModeStdioPrimary }

func (s stdioSource) Register(led *ledger.Ledger, key ledger.SourceKey, src logtypes.SourceIdentity) error {
	// 带 Path 的受管 Raw 走文件游标机制，分段由 FileTailer 登记；
	// 无 Path 的纯 Reader 才需要在这里登记一个逻辑分段。
	if s.p.tailer != nil {
		return nil
	}
	return led.RegisterSegment(key, ledger.Segment{
		Path:          "stdio://managed-raw",
		Kind:          ledger.SegmentStdio,
		ParserVersion: src.ParserVersion,
	})
}

func (s stdioSource) Poll() ([]logtypes.Event, error) {
	if s.p.tailer != nil {
		return s.p.pollFile()
	}
	return s.p.pollStdio()
}

// pushSource PUSH_ONLY：不主动拉取，事件由外部注入。
//
// 它**不实现 Poller**（见 Poller 的注释）。
type pushSource struct {
	p *Pipeline
}

func (s pushSource) Mode() AcquireMode { return ModePushOnly }

func (s pushSource) Register(led *ledger.Ledger, key ledger.SourceKey, src logtypes.SourceIdentity) error {
	// 纯逻辑分段：不绑定文件路径，只给账本一个归属，使回收/对账口径有起点。
	return led.RegisterSegment(key, ledger.Segment{
		Path:          "push://" + key.LogSourceID + "/" + key.SourceGeneration,
		Kind:          ledger.SegmentStdio,
		ParserVersion: src.ParserVersion,
	})
}

// sourceFor 按模式选来源实现。
//
// 这里保留了模式到实现的映射，但**只在这里一次**——新增来源时改这一处即可，
// 不必再碰 pollLocked 的 switch 或构造函数的分支。
func sourceFor(mode AcquireMode, p *Pipeline) (Source, error) {
	switch mode {
	case ModeFilePrimary:
		return fileSource{p: p}, nil
	case ModeStdioPrimary:
		return stdioSource{p: p}, nil
	case ModePushOnly:
		return pushSource{p: p}, nil
	default:
		return nil, fmt.Errorf("pipeline: unsupported mode %q", mode)
	}
}

// 编译期断言：三种拉取型来源都满足 Poller。
var (
	_ Poller = fileSource{}
	_ Poller = stdioSource{}
	_ Source = pushSource{}
)

// 确保 io 被引用（stdioSource 间接依赖 io.Reader 语义）。
var _ io.Reader
