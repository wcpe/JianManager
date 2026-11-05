// 本文件是**投影写入**面：writeProjection{,Plan,Day} 这条「重建/补写」路径，
// 以及为它服务的去重与事件段落盘（appendEvents / missingFromEventStore /
// migrateInlineEvents）。
//
// 注意：这条路径与 runtime_delivery_pipeline.go 的投递路径**共享底层**
// （appendEvents、insertInBatches），但语义契约不同（投递是「写进去就算」，
// 重建是「写进去还要逐字段校验并发布」）。改动其中一侧时必须检查另一侧。
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 部分插入/响应未知只有完成有效全字段查询后才可称实测missing；错误、限流、预算耗尽均保持未知。
// projectionWritePlan 描述一次投影写入的「写入面」与「发布语义」（FR-497 spec §2.2）。
//
// 为什么要拆开：重启恢复原先只有「整窗重发」一种形态——Replace=true 时把写入面**扩为
// 权威全量**。启动增量对账需要「只写缺失天，但发布语义仍是取代」（新代包含该天全量 →
// 取代安全），于是写入面与发布语义必须能分别指定。
type projectionWritePlan struct {
	// Write 是本次实际写入 VL 的事件集合。
	Write []logtypes.Event
	// Archive 是同时落归档的权威事件集合（现状恢复路径为 nil）。
	Archive []logtypes.Event
	// Replace 为 true 表示发布语义是「新代取代旧代白名单」（该天）；false 表示累积微代次。
	Replace bool
	// 正常尾采追加不得清掉较早历史窗口的待发布责任。
	KeepPending bool
	// 正常必要压缩与失败重建分别计copies，但共享持久source/day总字节硬顶。
	MaterializationClass string
	// Days 非空表示**限定写入面**为这些 UTC 天（增量补天）：此时 Replace 不再把写入面扩为
	// 权威全量，且 Days 必须与 Write 的天集合完全一致——错位会「标记为已发布却没写数据」，
	// 故按硬失败处理（宁可启动失败，不接受静默丢数据）。
	Days []string
	// Tail 非 nil 表示本次是「权威集合的增量尾部」：段存储按增量追加、发布水位走缓存，
	// 不需要（也没有）权威全量（判据与安全论证见 deliveryTailPlan）。
	Tail *deliveryTailPlan
	// Written仅允许从原WAL重新捕获的实际written窗口走只读确认，不能成为新的插入面。
	Written *writtenAttempt
	// 仅既有预算worker对同一实测missing窗口可携此能力；普通重放/其他日不能覆盖未知hint。
	WrittenRepair *writtenAttempt
	// Snapshot 必须在读取 canonical 集合之前取得，跨源更新不使它失效。
	Snapshot *publicationSnapshot
	// Yield 非 nil 时是**批间让出**钩子：每完成一个插入批、以及每个校验簇开始之前调用一次。
	//
	// 为什么放在计划里而不是全局开关：让出是**调用方**的节奏选择，同一份写入代码在采集快路径
	// （走 `deliver`，不该被额外 sleep 拖慢）与重建慢路径（必须给采集让路）上要有不同行为。
	// 生产采集路径恒为 nil（零开销）；只有重建调度器会填它。
	//
	// **安全边界**：让出只是"插入批与批之间、校验簇与簇之间"的主动退让，**不改变**任何写入面、
	// 校验判据、发布判据与错误处理——即它无法把失败变成成功，也无法把成功变成失败。
	Yield func()
}

// publicationSnapshot 绑定源/日的发布版本，与物理代次尝试号独立。
type publicationSnapshot struct {
	epochs               map[string]uint64
	ctx                  context.Context
	materializationClass string
	checkPendingWAL      bool
	written              *writtenAttempt
	keepPending          bool
	earlierParent        *writtenAttempt
}

var errPublicationStale = errors.New("ingest: 本源发布快照已失效，需重新规划")

func (m *Manager) capturePublicationSnapshot(source SourceConfig, contexts ...context.Context) *publicationSnapshot {
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	// 关停 Flush 发生在 lifecycleCancel 之后，正常投递保留既有独立完成语义。
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out := &publicationSnapshot{epochs: map[string]uint64{}, ctx: ctx}
	for day, epoch := range m.publicationEpochs[source.LogSourceID+"/"+source.SourceGeneration] {
		out.epochs[day] = epoch
	}
	return out
}

// 此检查只比较同源同日版本，禁止把其他源正常发布当成冲突。
func (m *Manager) publicationSnapshotCurrentLocked(source SourceConfig, snapshot *publicationSnapshot, days []string) bool {
	if snapshot == nil {
		return true
	}
	current := m.publicationEpochs[source.LogSourceID+"/"+source.SourceGeneration]
	if days == nil {
		for day, epoch := range current {
			if snapshot.epochs[day] != epoch {
				return false
			}
		}
		for day, epoch := range snapshot.epochs {
			if current[day] != epoch {
				return false
			}
		}
		return true
	}
	for _, day := range days {
		if snapshot.epochs[day] != current[day] {
			return false
		}
	}
	return true
}

// verifyProjectionChunkSize 是「发布确认快路径」的分批规模：与既有投影校验的批形一致（2000/批），
// 既避免单次查询过大，也避免一次性把整份证据塞进单条查询。
const verifyProjectionChunkSize = 2000

// RepublishSkipped 返回「发布确认快路径」累计跳过整窗重发的次数（观测用，单位=次）。
func (m *Manager) RepublishSkipped() int64 { return m.republishSkipped.Load() }

// eventSetDigest 计算一个事件集合的内容指纹（身份 + 内容哈希 + 记录区间）。
//
// 用途仅是**备忘的键**：用于回答「这次问的集合与上次已核对通过的是不是同一份」。
// 用 sha256 而不是短哈希：一次假命中会让本该追加的事件被跳过（静默丢数据），
// 属红线，故不接受任何可计算的碰撞风险（本函数只在内存里遍历，不做磁盘 I/O）。
func eventSetDigest(events []logtypes.Event) [sha256.Size]byte {
	h := sha256.New()
	var buf [8]byte
	for i := range events {
		h.Write([]byte(events[i].EventID))
		h.Write([]byte{0})
		h.Write([]byte(events[i].CanonicalHash))
		h.Write([]byte{0})
		binary.LittleEndian.PutUint64(buf[:], events[i].Record.Start)
		h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], events[i].Record.End)
		h.Write(buf[:])
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// eventStoreCoverage 是「这个内容指纹的权威集合已被段逐条覆盖」的备忘条目。
type eventStoreCoverage struct {
	// digest 是已核对通过的权威集合内容指纹（见 eventSetDigest）。
	//
	// 为什么必须按内容而不是按长度：不同调用方会传**不同**的集合（投递主路径传权威全量、
	// commitBackfill 传补账批、migrateInlineEvents 传内联旧格式），它们的长度可能相同而内容
	// 完全不同 —— 按长度命中就会跳过本该追加的事件（静默丢数据 ✗）。
	digest [sha256.Size]byte
	// rows / epoch 是记下备忘时的段读数：段物理行数与本源的段物理版本号。
	//
	// 为什么这两个就够：段是只追加结构，除「末段半行截断」（崩溃残留校准）外没有改写路径。
	//   - 真正追加 ⇒ 行数变（且 epoch 变）⇒ 备忘自动失效；
	//   - 末段截断 ⇒ 行数变（变小）⇒ 备忘自动失效；
	//   - **头部回收（eventstore.Prune）⇒ 行数变（变小，见 eventstore_prune.go）⇒ 备忘自动失效**，
	//     且回收成功后调用方会调 invalidateEventStoreRowCaches 推进 epoch、直接删掉本条备忘 ——
	//     两道保险，因为「回收后又追加回同样行数」这种形态只有 epoch 能区分；
	//   - 三者都没变 ⇒ 段内容逐字节没变（由 eventstore.calibrateLocked 的校准语义保证：
	//     清单计数恒等于物理完整行数，故行数相同即内容相同）。
	rows  int
	epoch uint64
}

// insertBatchMaxEvents / insertBatchMaxBytes / insertBatchFlushTimeout 等攒批常量已移到
// insert_batch.go（双阈值攒批层），本文件只保留调用点。
//
// 插入面有两个入口：insertInBatches（按攒批阈值分批投递，返回最后一批的 HTTP 状态；
// 任一批失败即中断并原样返回该批状态与错误，调用方据此判定投递结果）与
// dropEventsAlreadyWritten（写面幂等过滤，见下）。
