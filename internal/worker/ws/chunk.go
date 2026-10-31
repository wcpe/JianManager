package ws

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// 探针上行分片帧的重组（§2.5）。
//
// ## 为什么在 Worker 的 WS 读循环里做
//
// ServerProbe 的 FrameChunker 把**超阈值（bridge.chunk.max-frame-bytes）的整帧**切成
// `type=event` + `event=chunk` 的多片，经**同一条反向 WS** 上行；每片带
// `groupId`/`seq`/`total`/`ofType`/`requestId`，`data` 是整帧 UTF-8 字节的 base64 切片
// （按 4 的整数倍切，故每片可独立解码、整组拼接后字节完全一致）。
//
// 这意味着分片是**传输层**语义而不是业务语义：
//   - 被分片的可能是 `command_result`（命令回执，需要按 requestId 投递给同步等待者），
//     也可能是 `activity_change` 之类的业务事件；
//   - 下游（command_result 路由、事件扇出）拿到的必须是**完整原帧**，不能自己感知分片；
//   - 若把分片冒泡到 CP 再重组，CP 就得为每个节点维护重组状态，且 command_result
//     的同步往返会绕一圈——那等于把传输层细节泄漏到应用层。
//
// 因此重组在 Worker 的 WS 读循环内、任何语义分发之前完成：重组好的原帧文本原样
// 重新进入既有的 event 分发路径（command_result 投递 / 业务事件扇出），下游零改动。
//
// ## 有界性（spec §4 待定项 1）
//
// 分片帧来自已通过 JWT 校验的探针会话，但它仍是**对端可控的输入**：一个 bug 或恶意实现
// 可以只发 `seq 0` 就停住，让每实例堆满半组。三条硬上限防止内存无界：
//   - 单实例并行重组数 [ChunkMaxParallelPerInstance]（超出的整组拒收，不做部分入账）；
//   - 单组片数上限 [chunkMaxChunksPerGroup]；
//   - 单组解码后原始帧字节上限 [chunkMaxFrameBytes]（片进组时按 base64 长度折算累计）。
//
// 另有 [ChunkAssembleTimeout] 兜底：不完整的组超时即整组丢弃并记 `INCOMPLETE_CHUNK`，
// 绝不长期滞留（探针侧的 FrameGroupQueue 也是"整组要么全进要么全不进"，对端不会
// 依赖半组存在，所以丢弃是安全的）。
const (
	// chunkEventName 探针上行分片帧的 event 子类型（与 ServerProbe FrameChunker.EVENT_CHUNK 一致）。
	chunkEventName = "chunk"

	// ChunkMaxParallelPerInstance 单实例最大并行重组数（spec §2.5 默认 8）。
	ChunkMaxParallelPerInstance = 8

	// ChunkAssembleTimeout 单组重组超时（spec §2.5 默认 30s）。
	ChunkAssembleTimeout = 30 * time.Second

	// chunkMaxChunksPerGroup 单组片数上限。
	//
	// 探针侧 `bridge.chunk.max-chunks-per-group` 是同一概念的对端防线；这里取一个
	// 明显更宽的值（探针默认量级是几十片），只拦"根本没打算拼完"的输入。
	chunkMaxChunksPerGroup = 4096

	// chunkMaxFrameBytes 单组解码后的原始帧字节上限（8 MiB）。
	//
	// 分片存在的理由就是原帧超过单帧阈值，故这里必须比阈值宽得多；同时它把
	// "每实例 8 组 × 8 MiB" 的总内存钉死在 64 MiB 量级。
	chunkMaxFrameBytes = 8 << 20

	// chunkSweepMinInterval 惰性清扫的最小间隔。
	//
	// 不为清扫单独起 goroutine：读循环每次收片时顺带清扫一次（全表遍历，组数 ≤ 8×实例数），
	// 节流到 1s 以免密集分片时反复遍历。代价是"最后一片之后再无分片"时残留组会多留一会儿，
	// 但组数本身有界，且下一次任何实例的分片都会清掉它。
	chunkSweepMinInterval = time.Second
)

// 分片重组错误。全部为"整组拒收/丢弃"语义：宁可明确失败，也不要半组。
var (
	// ErrChunkSeqOutOfRange seq 不在 [0,total) 内（对端实现错误）。
	ErrChunkSeqOutOfRange = errors.New("分片序号越界")
	// ErrChunkBadTotal total 非正数。
	ErrChunkBadTotal = errors.New("分片总数非法")
	// ErrChunkTooManyGroups 本实例并行重组数已达上限。
	ErrChunkTooManyGroups = errors.New("并行重组数超上限")
	// ErrChunkTooManyChunks 单组片数超上限。
	ErrChunkTooManyChunks = errors.New("单组分片数超上限")
	// ErrChunkFrameTooLarge 单组重组后原始帧超字节上限。
	ErrChunkFrameTooLarge = errors.New("单组重组体积超上限")
	// ErrChunkBadBase64 片数据不是合法 base64。
	ErrChunkBadBase64 = errors.New("分片数据非法 base64")
	// ErrChunkEmpty 组内无任何有效片（理论不可达：完成必然至少一片）。
	ErrChunkEmpty = errors.New("分片组为空")
)

// chunkRequest 一帧上行分片的信封字段（字段名与 ServerProbe FrameChunker.chunkJson 一致）。
//
// `data` 在这里是 base64 文本而不是 RawMessage：片数据只含 base64 字母，
// 按字符串取比让 encoding/json 先解引号再反转义更直接。
type chunkRequest struct {
	GroupID   string `json:"groupId"`
	Seq       int    `json:"seq"`
	Total     int    `json:"total"`
	OfType    string `json:"ofType"`
	RequestID string `json:"requestId"`
	Data      string `json:"data"`
}

// chunkKey 标识一个重组组：实例与会话绑定，避免不同实例的 groupId 撞号（groupId 是
// 探针侧新生成的 UUID，理论上不会撞，但"实例"维度是隔离的硬边界，不该靠对端保证）。
type chunkKey struct {
	instance string
	group    string
}

// chunkGroup 一个在途重组组。
type chunkGroup struct {
	chunks [][]byte // 按 seq 索引；nil = 该片尚未到达
	seen   []bool   // 逐 seq 的到达标记（重复片覆盖不重复计数）
	recv   int
	total  int
	ofType string
	bytes  int // 已收片折算的**解码后**字节数（用于上限判定）
	start  time.Time
	last   time.Time
}

// chunkDrop 一次超时丢弃的明细，供冒泡 `INCOMPLETE_CHUNK` 事件。
type chunkDrop struct {
	Instance string
	GroupID  string
	OfType   string
	Received int
	Total    int
	Reason   string
}

// chunkAssembler 按实例 + groupId 重组上行分片帧。
//
// 并发模型：读循环是唯一写入方，但仍全套加锁——清扫与计数可能被观测口读，
// 且把可见性寄托在"目前只有一个调用方"上，将来一改就炸。
type chunkAssembler struct {
	mu      sync.Mutex
	groups  map[chunkKey]*chunkGroup
	perInst map[string]int // 每实例在途组数

	lastSweep time.Time
	timeout   time.Duration
	now       func() time.Time

	// 累计计数（观测/排障用；契约里没有对应字段，故只进日志与测试）。
	incomplete int64 // INCOMPLETE_CHUNK：超时丢弃的组数
	rejected   int64 // 因超上限/非法信封被拒的组数
	completed  int64 // 成功重组的组数
}

// newChunkAssembler 创建重组器。
func newChunkAssembler() *chunkAssembler {
	return &chunkAssembler{
		groups:  make(map[chunkKey]*chunkGroup),
		perInst: make(map[string]int),
		timeout: ChunkAssembleTimeout,
		now:     time.Now,
	}
}

// accept 收下一片，若整组到齐则返回重组出的原始帧文本（逐字节等于探针被切前的 payload）。
//
// 返回 nil 帧表示"尚未完成，继续等"；返回 error 表示该组已被拒收/丢弃，
// 调用方只需记日志（绝不断连：坏片不该拖垮整条桥）。
//
// **不做超时清扫**：清扫由调用方在收片后统一触发（[sweep]），职责单一。
func (a *chunkAssembler) accept(instance string, req chunkRequest) ([]byte, error) {
	// 信封校验在锁外做，只锁住计数自增。
	if req.Total <= 0 {
		a.bumpRejected()
		return nil, fmt.Errorf("%w: total=%d", ErrChunkBadTotal, req.Total)
	}
	if req.Seq < 0 || req.Seq >= req.Total {
		a.bumpRejected()
		return nil, fmt.Errorf("%w: seq=%d total=%d", ErrChunkSeqOutOfRange, req.Seq, req.Total)
	}
	if req.Total > chunkMaxChunksPerGroup {
		a.bumpRejected()
		return nil, fmt.Errorf("%w: total=%d 上限=%d", ErrChunkTooManyChunks, req.Total, chunkMaxChunksPerGroup)
	}
	// 单片解码后字节数：base64 每 4 字符对应 3 字节。
	chunkBytes := len(req.Data) / 4 * 3
	if chunkBytes > chunkMaxFrameBytes {
		a.bumpRejected()
		return nil, fmt.Errorf("%w: 单片约 %d 字节", ErrChunkFrameTooLarge, chunkBytes)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()

	key := chunkKey{instance: instance, group: req.GroupID}
	grp, ok := a.groups[key]
	if !ok {
		// 新组：并行重组数是本实例维度的硬上限，超了整组拒收（不建表项，避免半组占位）。
		if a.perInst[instance] >= ChunkMaxParallelPerInstance {
			a.rejected++
			return nil, fmt.Errorf("%w: 实例 %s 在途组数 %d", ErrChunkTooManyGroups, instance, a.perInst[instance])
		}
		grp = &chunkGroup{
			chunks: make([][]byte, req.Total),
			seen:   make([]bool, req.Total),
			total:  req.Total,
			ofType: req.OfType,
			start:  now,
			last:   now,
		}
		a.groups[key] = grp
		a.perInst[instance]++
	} else if grp.total != req.Total {
		// 同组 total 不一致：对端实现错误或 groupId 被复用。整组作废，
		// 让后续片重新起一组（旧组已删，不会无限增长）。
		a.dropLocked(key)
		a.rejected++
		return nil, fmt.Errorf("%w: 组 %s 的 total 由 %d 变为 %d", ErrChunkBadTotal, req.GroupID, grp.total, req.Total)
	}

	// 重复片（探针重发）：覆盖数据但不重复计数，避免"到齐"判定被虚高的 recv 提前触发。
	if grp.seen[req.Seq] {
		prev := len(grp.chunks[req.Seq]) / 4 * 3
		grp.chunks[req.Seq] = []byte(req.Data)
		grp.bytes += chunkBytes - prev
		grp.last = now
		return nil, nil
	}
	grp.chunks[req.Seq] = []byte(req.Data)
	grp.seen[req.Seq] = true
	grp.recv++
	grp.bytes += chunkBytes
	grp.last = now
	if grp.bytes > chunkMaxFrameBytes {
		a.dropLocked(key)
		a.rejected++
		return nil, fmt.Errorf("%w: 组 %s 累计约 %d 字节", ErrChunkFrameTooLarge, req.GroupID, grp.bytes)
	}
	if grp.recv < grp.total {
		return nil, nil
	}

	// 到齐：按 seq 拼接 base64 → 一次性解码，得到与探针切分前**逐字节相同**的原帧。
	joined := make([]byte, 0, grp.bytes/3*4)
	for _, part := range grp.chunks {
		joined = append(joined, part...)
	}
	raw, err := base64.StdEncoding.DecodeString(string(joined))
	a.dropLocked(key)
	if err != nil {
		a.rejected++
		return nil, fmt.Errorf("%w: 组 %s", ErrChunkBadBase64, req.GroupID)
	}
	if len(raw) == 0 {
		a.rejected++
		return nil, ErrChunkEmpty
	}
	a.completed++
	return raw, nil
}

// bumpRejected 只自增拒收计数（供锁外的信封校验分支使用）。
func (a *chunkAssembler) bumpRejected() {
	a.mu.Lock()
	a.rejected++
	a.mu.Unlock()
}

// dropLocked 删除一个组并归还其占位。调用方须持 a.mu。
func (a *chunkAssembler) dropLocked(key chunkKey) {
	if _, ok := a.groups[key]; !ok {
		return
	}
	delete(a.groups, key)
	if n := a.perInst[key.instance] - 1; n > 0 {
		a.perInst[key.instance] = n
	} else {
		delete(a.perInst, key.instance)
	}
}

// sweep 清扫过期组，返回被丢弃的明细（供调用方记 INCOMPLETE_CHUNK）。
//
// 节流到 [chunkSweepMinInterval]：密集分片时不必每片都遍历全表。
// 单测把注入时钟往前推即可越过节流窗口。
func (a *chunkAssembler) sweep() []chunkDrop {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if !a.lastSweep.IsZero() && now.Sub(a.lastSweep) < chunkSweepMinInterval {
		return nil
	}
	a.lastSweep = now
	return a.sweepAllLocked(now)
}

// sweepNow 无视节流立即清扫，供会话结束等"必须马上回收"的场景与单测直接驱动。
func (a *chunkAssembler) sweepNow() []chunkDrop {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastSweep = a.now()
	return a.sweepAllLocked(a.lastSweep)
}

// sweepAllLocked 遍历全表逐组判定超时。调用方须持 a.mu。
func (a *chunkAssembler) sweepAllLocked(now time.Time) []chunkDrop {
	var drops []chunkDrop
	for key, grp := range a.groups {
		if now.Sub(grp.last) < a.timeout {
			continue
		}
		drops = append(drops, chunkDrop{
			Instance: key.instance,
			GroupID:  key.group,
			OfType:   grp.ofType,
			Received: grp.recv,
			Total:    grp.total,
			Reason:   "INCOMPLETE_CHUNK",
		})
		a.dropLocked(key)
		a.incomplete++
	}
	return drops
}

// dropInstance 丢弃某实例的全部在途组（会话结束时调用），返回明细供记 INCOMPLETE_CHUNK。
func (a *chunkAssembler) dropInstance(instance string) []chunkDrop {
	a.mu.Lock()
	defer a.mu.Unlock()
	var drops []chunkDrop
	for key, grp := range a.groups {
		if key.instance != instance {
			continue
		}
		drops = append(drops, chunkDrop{
			Instance: key.instance,
			GroupID:  key.group,
			OfType:   grp.ofType,
			Received: grp.recv,
			Total:    grp.total,
			Reason:   "INCOMPLETE_CHUNK",
		})
		a.dropLocked(key)
		a.incomplete++
	}
	return drops
}

// stats 返回累计计数快照（completed / incomplete / rejected），供观测与测试。
func (a *chunkAssembler) stats() (completed, incomplete, rejected int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.completed, a.incomplete, a.rejected
}

// pending 返回当前在途组数（测试与排障用）。
func (a *chunkAssembler) pending(instance string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.perInst[instance]
}

// logChunkDrop 记录被丢弃的组。
//
// 不返回、不重试：分片超时本质是"对端没发完"，重试也无从重试（Worker 无法请求重发某一组）。
// 载入日志是为了能回答"这条事件为什么没到"，这正是 spec 要求记 INCOMPLETE_CHUNK 的目的。
func logChunkDrop(d chunkDrop) {
	slog.Warn("插件桥分片重组超时，整组丢弃",
		"instanceId", d.Instance,
		"groupId", d.GroupID,
		"ofType", d.OfType,
		"received", d.Received,
		"total", d.Total,
		"code", d.Reason,
	)
}
