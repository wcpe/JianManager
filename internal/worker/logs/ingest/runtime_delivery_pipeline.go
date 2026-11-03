// 本文件是**投递的写入与校验**面：攒批写入（insertInBatches*）、写前幂等
// （dropEventsAlreadyWritten*）、按天分组、以及 VL 逐字段校验的完整链路
// （verifyProjection*、verifyBudget*、verifyProjectionQueryInWindowCore）。
//
// 校验的判据是「同一段区间内逐字段可见」，不是「末端水位够了就算」——
// 见 verifyProjectionQueryInWindowCore 的注释，那里解释了为什么不能取凸包。
package ingest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// dropEventsAlreadyWritten 对「已写入过的同 id 同 hash」做 id 级幂等过滤
// （w21 ② 第四落点，2026-10-10 扩展到 replace 路径）：≤500 个 event_id 一批查询，
// 命中则从本次**插入面**剔除 ✓（调用方保持校验面为全量，见 writeProjectionDay 的说明）。
//
// generation 参数决定查询标签面（两个口径共用同一实现，不新造平行逻辑 ✓）：
//   - generation 非空 ⇒ 只认**该物理代次名**（`projection_generation:=<generation>`）——
//     供 replace 路径掐掉「同名代次被写两次」；
//   - generation 为空 ⇒ 不限代次标签（只按源 + 源代次）——保持既有语义，
//     认「任意已写入过的同 id 同 hash」（含更早的未发布 staging 代次），供慢路径使用。
//
// 语义与安全：
//   - 调用点只覆盖**非尾部**路径（replace 与慢路径）⇒ 常规增量尾部零开销 ✓；
//   - 任何查询失败/权威缺失一律**照写**（宁可多写不漏，绝不因缺证据而少写 ✓）；
//   - 只跳过同 id **且**同 hash ⇒ 同 id 异 hash 的真更正照写 ✓；
//   - 调用侧**不得**加 `expectedAuthority != nil` 之类早短路：分区记录暂缺（重启早期）与幂等无关 ✗。
func (m *Manager) dropEventsAlreadyWritten(client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) []logtypes.Event {
	return m.dropEventsAlreadyWrittenCtx(m.lifecycleContext(), client, source, generation, events)
}

func (m *Manager) dropEventsAlreadyWrittenCtx(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) []logtypes.Event {
	if client == nil || len(events) == 0 {
		return events
	}
	written := make(map[string]string, len(events))
	for start, end := 0, 0; start < len(events); start = end {
		var chunkErr error
		end, chunkErr = scopedProjectionChunkEnd(source, []string{generation}, events, start)
		if chunkErr != nil {
			return events
		}
		if end-start > insertBatchMaxEvents {
			end = start + insertBatchMaxEvents
		}
		ids := make([]string, 0, end-start)
		seenID := make(map[string]bool, end-start)
		for _, event := range events[start:end] {
			if event.EventID == "" || seenID[event.EventID] {
				continue
			}
			seenID[event.EventID] = true
			ids = append(ids, strconv.Quote(event.EventID))
		}
		if len(ids) == 0 {
			continue
		}
		filter := fmt.Sprintf("log_source_id:=%q AND source_generation:=%q",
			source.LogSourceID, source.SourceGeneration)
		// **不再按 `projection_generation` 过滤**（2026-11-02 投递路径简化的直接后果）。
		//
		// 本函数原先把非空 generation 拼成 `AND projection_generation:=<代次名>`，用来掐掉
		// 「同一代次名被写两次」。但简化后 `projectionPayload` **不再写这个标签**
		// （见该函数的注释：一批数据只写一次，不再有按批次过滤的需要）。
		// 于是这条选择器在真实数据上永远不命中 —— 实测：写入明明成功、行就在 VL 里，
		// 本过滤仍把三条事件原样返回（TestDropEventsAlreadyWritten_SameID_SameHashDropped 红）。
		//
		// 去掉它会不会放宽判据？不会：本函数的安全方向由**同 id 且同 hash** 这个双重判据承担
		// （下方 kept 循环），代次维度只是「在哪些行里找同 id」的缩小面。不缩小查询面，
		// 最坏结果是多看见几条历史同 id 行 —— 而它们要么 hash 相同（本来就该跳过），
		// 要么 hash 不同（真更正，本来就该照写）。方向与「宁可多写不漏」一致。
		_ = generation
		query := fmt.Sprintf("%s AND event_id:in(%s) | fields event_id, canonical_content_hash | limit 20000",
			filter, strings.Join(ids, ","))
		if len(query) > scopedProjectionQueryMaxBytes {
			return events
		}
		// **必须显式给时间窗**：不带 start/end 的查询会被 VL 以默认全时段
		// （[1677-09-21, 2262-04-11]）处理，并被 `-search.maxQueryTimeRange` 以 400 拒绝
		// （2026-10-08 实测记录见 vlsup/cmdline.go）⇒ 本函数走「证据缺失照写」⇒
		// 幂等过滤整条失效，且**每次分片都会失败**（identityChunk=500，单源段数十万行即数百次）。
		// 该幂等要掐掉的正是「同名代次被写两次 ⇒ 该代次条数翻倍」（生产实测 544205×2、
		// VL 条数/唯一事件 3.45x）——失效后该放大原样复现。
		//
		// 窗口按本批事件的**自身时间**取（±1s 裕量）。
		//
		// **刻意不夹 `source.UTCDay` 日界**（与写后逐字段校验的做法不同）：校验查询按
		// `log_source_id + source_generation + projection_generation` 取**整批行集**，越界会把
		// 邻日批次的日界事件带进结果集而误判 duplicate（故校验面必须夹）；本查询只按
		// `event_id:in(本批 ids)` 取 hash，越界事件的 event_id 不在本批 ids 内、不可能命中，
		// 故夹日界无收益。反而有害：重放路径的 `source.UTCDay` 与事件所属日**可以不同**
		// （`daySourceFor` 正是为此把 UTCDay 设为事件日；实时源则取当天），夹住会把合法的
		// 历史重放事件排除在窗口外 ⇒ 幂等过滤对它们静默失效（本项要防的形态）。
		var winStart, winEnd time.Time
		for _, event := range events[start:end] {
			when, ok := parseEventTimeUTC(event.EventTimeUTC)
			if !ok {
				continue
			}
			if winStart.IsZero() || when.Before(winStart) {
				winStart = when
			}
			if winEnd.IsZero() || when.After(winEnd) {
				winEnd = when
			}
		}
		if winStart.IsZero() {
			// 本片无可用事件时间：**不给窗口会必然被 VL 拒**（见上），故退化为「跳过本片查询、
			// 照写」——与「证据缺失照写」同方向（宁可多写不漏），且不发出必然失败的请求。
			continue
		}
		winStart = winStart.Add(-time.Second)
		winEnd = winEnd.Add(time.Second)
		params := url.Values{
			"query": {query},
			"start": {winStart.UTC().Format(time.RFC3339Nano)},
			"end":   {winEnd.UTC().Format(time.RFC3339Nano)},
		}
		err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
			scanner := bufio.NewScanner(io.LimitReader(body, 32<<20))
			scanner.Buffer(make([]byte, 64*1024), 8<<20)
			for scanner.Scan() {
				var row struct {
					EventID string `json:"event_id"`
					Hash    string `json:"canonical_content_hash"`
				}
				if json.Unmarshal(scanner.Bytes(), &row) == nil && row.EventID != "" {
					written[row.EventID] = row.Hash
				}
			}
			return scanner.Err()
		})
		if err != nil {
			return events // 证据缺失：照写（宁可多写不漏 ✓）
		}
	}
	if len(written) == 0 {
		return events
	}
	kept := make([]logtypes.Event, 0, len(events))
	skipped := 0
	for _, event := range events {
		if hash, ok := written[event.EventID]; ok && hash == event.CanonicalHash {
			skipped++
			continue
		}
		kept = append(kept, event)
	}
	if skipped > 0 {
		// 可观测性：跳过条数并入既有 republishSkipped（与"确认快路径"的跳过同口径 ✓）。
		m.republishSkipped.Add(int64(skipped))
		if generation != "" {
			slog.Info("同代次重写：已存在同 id 同 hash，跳过重复插入（校验面不缩小）",
				"generation", generation, "source", source.LogSourceID,
				"planned", len(events), "skipped", skipped, "toInsert", len(kept))
		}
	}
	return kept
}

func (m *Manager) insertInBatches(client *vlsup.Client, events []logtypes.Event, generation string) (int, error) {
	return m.insertInBatchesCtx(context.Background(), client, events, generation)
}

func (m *Manager) insertInBatchesCtx(ctx context.Context, client *vlsup.Client, events []logtypes.Event, generation string) (int, error) {
	return m.insertInBatchesCtxYield(ctx, client, events, generation, nil)
}

// insertInBatchesCtxYield 是插入的**限速形态**：每完成一个批调用一次 yield（可为 nil）。
//
// 2026-10-31 改造（对齐 Vector / Filebeat）：本函数现在是 insert_batch.go 那层的**薄入口**——
// 先把事件按「4 MiB 字节 / 500 条 / 1s 时间」三阈值（先到先发）攒成批，再由 insertBatchWorkers
// 个 worker **并发**投递，每批失败按指数退避有界重试。改造前这里是「固定 500 条、串行、无重试」。
//
// 为什么需要批间让出（2026-10-09 生产实证）：单天 57 万事件 = 1147 个 500 条批，原文一次跑完，
// 期间（连同紧随其后的 287 簇校验）连续吃满算力、把采集轮挤在身后。批间让出把这次重建
// 从「连续吃满」摊成「间歇性让路」，而让出期间**不持任何锁**。
//
// 不变量（安全论证，逐条保持）：
//   - yield 只发生在**批与批之间**，不插入到某个批的请求内部 ⇒ 「一个批要么整批成功、要么
//     整批失败」的语义逐字不变；
//   - **派发顺序 = 事件顺序**（worker 从 FIFO 按 seq 取批），因此同源批的发出次序与事件次序一致，
//     `record_start/end` 的连续性判定不受影响；只有完成次序可能交错，而结果本就按区间并集记账；
//   - 取消即收手：已写入的批**保持已写**（VL 幂等由 event_id 承担），调用方据此判定「未完成」，
//     不得当成发布成功（见 rebuildDays 的返回值语义）。
func (m *Manager) insertInBatchesCtxYield(ctx context.Context, client *vlsup.Client, events []logtypes.Event, generation string, yield func()) (int, error) {
	limits := m.insertBatchLimitsOf()
	batches, err := m.accumulateInsertBatches(events, generation, limits)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		// 取消即收手（一个请求都不发）：与改造前「首批之前就检查一次」同语义。
		return 0, err
	}
	return m.dispatchInsertBatches(ctx, client, batches, limits, yield)
}

func projectionPayload(events []logtypes.Event, generation string) ([]byte, error) {
	var b strings.Builder
	for _, event := range events {
		line := map[string]any{
			"_time": event.EventTimeUTC, "_msg": event.Message,
			"event_id": event.EventID, "log_source_id": event.Source.LogSourceID,
			"source_generation": event.Source.SourceGeneration,
			"parser_version":    event.Source.ParserVersion,
			"record_start":      event.Record.Start, "record_end": event.Record.End,
			"ingest_time_utc": event.IngestTimeUTC, "level": event.Level, "stream": event.Stream,
			"canonical_content_hash": event.CanonicalHash,
			// **不再写 `projection_generation`**（2026-11-02 简化投递路径）。
			//
			// 这个标签唯一的作用是让查询按「批次白名单」过滤，避免同一事件的多份副本查重。
			// 而它带来的是：同一事件在 VL 里堆 N 份（实测一个源 118 个代次、同一 event 9 份），
			// 白名单一旦只认最新代次，其余数据就从该源的读面上消失（实测 22334 条只见 1160 条）。
			//
			// 简化后**不再有重写**：一批数据只写一次，event_id 幂等保证重投不产生新行。
			// 因此不需要「按批次过滤」这个维度 —— 查询侧同步去掉（见 vlrange 的同批改动）。
			// 历史数据仍带该标签，但查询侧不再用它过滤，所以老数据照常可查。
		}
		for k, v := range event.Fields {
			line[k] = v
		}
		data, err := json.Marshal(line)
		if err != nil {
			return nil, err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// eventUTCDay 解析事件所属 UTC 日，与 normalizeCanonicalEvent 的时间策略一致：
// 优先事件语义时间，缺失/不可解析时回退 ingest 时间，再回退源配置的 UTCDay。
//
// 为什么必须回退而不是报错：真实日志行可能没有可解析的语义时间（解析失败或非标准格式）。
// 若此处直接报错，投递/投影路径会失败 → 受管恢复责任无法转移 → reclaim 永不推进 → WAL 保留
// 全部事件（真机 64 源实测：Worker RSS 涨到 2GiB、state 文件 285MB）。事件时间回退是既有契约行为。
func eventUTCDay(source SourceConfig, event logtypes.Event) (string, error) {
	for _, raw := range []string{event.EventTimeUTC, event.IngestTimeUTC} {
		if raw == "" {
			continue
		}
		if when, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return when.UTC().Format("2006-01-02"), nil
		}
	}
	if source.UTCDay != "" {
		return source.UTCDay, nil
	}
	return "", fmt.Errorf("ingest: event %s has no usable event/ingest time or partition day", event.EventID)
}

// canonicalEventTime 解析事件的规范时间：优先语义时间 event_time，缺失/不可解析时回退 ingest 时间。
// 与 normalizeCanonicalEvent 的时间策略一致（同一契约行为），避免「无解析时间的真实日志行」令
// 投递/校验路径失败而导致 reclaim 停滞。
func canonicalEventTime(event logtypes.Event) (time.Time, error) {
	for _, raw := range []string{event.EventTimeUTC, event.IngestTimeUTC} {
		if raw == "" {
			continue
		}
		if when, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return when.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("no usable event or ingest time")
}

func groupEventsByUTCDay(source SourceConfig, events []logtypes.Event) (map[string][]logtypes.Event, []string, error) {
	// 零让路包装：包外调用点逐字不变 ✓（让路只在常驻对账路径显式启用）。
	return groupEventsByUTCDayYielding(source, events, 0, 0, nil)
}

// groupEventsByUTCDayYielding 是**会主动让路**的分组实现（CPU 密集对账的节流口）。
//
// 为什么必须让路（2026-10-03 现场 X 光片）：`canonicalDayCounts→groupEventsByUTCDay` 对整源事件
// 做一次全量分组（单源可达数十万条），在 60 台/113 万条规模下与采集轮**抢核** ✗——采集轮
// 表现为"明明没等锁、也没等落库，却推进不动"。让路 = 每 slice 条 `time.Sleep(yield)`：
// 睡眠会把 P 交还调度器 ✓ 采集轮立即获得 CPU ✓；对账自身只多花 O(总条数/slice × yield) ✓
// （默认 4096 条 / 5ms：10 万条源 ≈ 120ms 额外延迟，可忽略 ✓）。
//
// slice<=0 或 yield<=0 ⇒ 零让路（等价于纯函数，测试可关 ✓）。
func groupEventsByUTCDayYielding(source SourceConfig, events []logtypes.Event, slice int, yield time.Duration, notify func()) (map[string][]logtypes.Event, []string, error) {
	grouped := make(map[string][]logtypes.Event)
	for i, event := range events {
		if yield > 0 && slice > 0 && i > 0 && i%slice == 0 {
			if notify != nil {
				notify()
			}
			time.Sleep(yield)
		}
		day, err := eventUTCDay(source, event)
		if err != nil {
			return nil, nil, err
		}
		grouped[day] = append(grouped[day], event)
	}
	days := make([]string, 0, len(grouped))
	for day := range grouped {
		days = append(days, day)
	}
	sort.Strings(days)
	return grouped, days, nil
}

// publishedClosedForSource 返回该源已发布投影的保守封闭前缀（所有受影响分区的最小封闭水位）。
// 受影响分区由事件集合的 UTC 日推导：归档导入/轮转可能跨日，事件自身才是权威来源。
//
// 事件体在磁盘段时按日聚合最大末端位置（流式，不载入全量事件）；旧格式仍用内联切片。
func (m *Manager) publishedClosedForSource(source SourceConfig, saved persistedSource) (uint64, bool) {
	// ctx 形态是**唯一实现**（见 publishedClosedForSourceCtx）：此处传 Background，保持既有签名
	// 与语义（既有调用点逐字不变）。投影读取失败在无 ctx 语义下仍表现为「不完整」。
	closed, complete, _ := m.publishedClosedForSourceCtx(context.Background(), source, saved)
	return closed, complete
}

// publishedClosedForSourceCtx 是可取消形态：ctx 在源内聚合的逐行回调处生效（Iterate 逐行回调，
// 故取消能即时中止段扫描，不必等整源读完），并在内联事件路径的每条事件前校验。
//
// 为什么必须可取消（2026-10-02 压测现场）：单源投影查询要扫该源的全部磁盘事件段（生产单源段
// 达数十万行、全库 18GB），一次整节点解算 = 75 次这样的扫描。此前它压在 cycleMu 上且不可取消；
// 现在它离锁执行，但**仍必须能被打断**——否则 HTTP 300s 超时之后，服务端还会继续为一份没人
// 再等的响应扫下去。
func (m *Manager) publishedClosedForSourceCtx(ctx context.Context, source SourceConfig, saved persistedSource) (uint64, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dayMaxEnd := map[string]uint64{}
	if saved.EventsStored && m.events != nil {
		key := source.LogSourceID + "/" + source.SourceGeneration
		scanRows := 0
		// **轮内预算**：单源扫描最多花 scanBudgetOf()（超时 ⇒ 证据不完整 ⇒ 不消解/不推进 ✓ 安全方向）。
		scanCtx, scanCancel := context.WithTimeout(ctx, m.scanBudgetOf())
		defer scanCancel()
		agg, err := m.events.DayMaxEndCtx(scanCtx, key, func(ev logtypes.Event) (string, error) {
			// **段读让路**（2026-10-03 现场 X 光片：readSegment 是每轮重活的 I/O 大头 ✗）：
			// 每 slice 行交还一次 P ⇒ 与之并行的采集轮不再被单源整段扫描压满 ✓。开销可忽略
			// （默认 1ms / 8192 行 ⇒ 10 万行源额外 ≈12ms ✓）；ctx 取消仍逐行生效 ✓（语义不变 ✓）。
			scanRows++
			if slice := m.scanSliceOf(); slice > 0 && scanRows%slice == 0 {
				if m.scanYieldNotify != nil {
					m.scanYieldNotify()
				}
				if y := m.scanYieldOf(); y > 0 {
					time.Sleep(y)
				}
			}
			return eventUTCDay(source, ev)
		})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				// 超预算：按"证据不完整"处理（**不是**错误 ⇒ 上层不消解缺口、不推进水位 ✓），
				// 下一轮继续扫；这样单源大扫描不会把整轮撑成分钟级 ✗。
				m.scanBudgetExceeded.Add(1) // 观测口：无条件的截断计数 ✓
				return 0, false, nil
			}
			return 0, false, err
		}
		dayMaxEnd = agg
	} else {
		for _, event := range saved.Events {
			if err := ctx.Err(); err != nil {
				return 0, false, err
			}
			day, err := eventUTCDay(source, event)
			if err != nil {
				return 0, false, err
			}
			if event.Record.End > dayMaxEnd[day] {
				dayMaxEnd[day] = event.Record.End
			}
		}
	}
	closed, complete := m.publishedClosedFromDayMax(source, dayMaxEnd)
	return closed, complete, nil
}

// publishedClosedFromDayMax 是「按日最大末端 + 已发布投影」推导保守封闭前缀的纯计算部分
// （与改动前的实现逐字相同，只是从 publishedClosedForSource 中抽出以便 ctx 形态复用）。
func (m *Manager) publishedClosedFromDayMax(source SourceConfig, dayMaxEnd map[string]uint64) (uint64, bool) {
	days := make([]string, 0, len(dayMaxEnd))
	for day := range dayMaxEnd {
		days = append(days, day)
	}
	sort.Strings(days)
	if len(days) == 0 && source.UTCDay != "" {
		days = []string{source.UTCDay}
	}
	if len(days) == 0 {
		return 0, false
	}

	var closed uint64
	for _, day := range days {
		key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day}
		rec, ok := m.cat.Get(key)
		if !ok || rec.PublishedProjection == nil || !rec.PublishedProjection.CoverageComplete {
			return 0, false
		}
		projection := rec.PublishedProjection
		dayClosed := uint64(0)
		if len(projection.SourceProjections) > 0 {
			found := false
			for _, scope := range projection.SourceProjections {
				if scope.LogSourceID == source.LogSourceID && scope.SourceGeneration == source.SourceGeneration {
					dayClosed, found = scope.ClosedVisibleSeq, true
					break
				}
			}
			if !found {
				return 0, false
			}
		} else {
			dayClosed = projection.ClosedVisibleSeq["default"]
			if sourceClosed := projection.ClosedVisibleSeq[key.String()]; sourceClosed > dayClosed {
				dayClosed = sourceClosed
			}
		}
		if dayMaxEnd[day] > dayClosed {
			return 0, false
		}
		if dayClosed > closed {
			closed = dayClosed
		}
	}
	return closed, true
}

// verifyProjection checks the actual VL query input before publishing a
// generation. JSON-stream request completion does not prove individual rows
// were accepted, and an uncertain target must never become query authority.
func (m *Manager) clientForSource(source SourceConfig) (*vlsup.Client, bool, error) {
	if m.vlRoute != nil {
		client, frozen, err := m.vlRoute(source)
		if err != nil {
			return nil, frozen, err
		}
		if client == nil {
			return nil, frozen, fmt.Errorf("ingest: Catalog-selected VictoriaLogs client is unavailable")
		}
		return client, frozen, nil
	}
	if m.vl == nil {
		return nil, false, fmt.Errorf("ingest: VictoriaLogs client is not ready")
	}
	return m.vl, false, nil
}

// 投影校验的退避默认值（B1c）。
//
// 上限由 2s 收紧到 500ms 的依据（2026-10-01 实测，探针见 docs/specs/log-verify-chunking/spec.md §6）：
// VL 的「写入 → 可查询」延迟**恒定**在 1011–1029 ms（20/20 次，见可见延迟探针），
// 而退避序列 200/400/800/封顶 2s 的探测点是 0 / 0.2 / 0.6 / **1.4** s —— 恰好在 1.0 s 附近
// 没有探测点，每次批级校验都白等约 0.4 s（真进程夹具批级 verify p50 = 1411 ms）。
// 封顶 500ms 后探测点变为 0 / 0.2 / 0.6 / 1.1 s，数据一可见就命中并返回。
//
// 为什么不是「固定 200ms 轮询」：2026-09-28 生产事故正是固定间隔轮询到 30 秒超时
// （单源最多约 150 次查询，把 VL 与磁盘一起压垮）。本改动保持**指数退避**语义，
// 只把封顶从 2s 收到 500ms——等待更久时（数据长期不可见）2 秒窗口内的查询次数是 5 次，
// 仍远低于事故时的量级；查询成本本身也已被分簇压到「每批 1 次」（见 verifyChunkEvents）。
const (
	defaultVerifyBackoffMin = 200 * time.Millisecond
	defaultVerifyBackoffMax = 500 * time.Millisecond
)

// nextVerifyBackoff 返回下一次退避时长：指数增长并封顶（B1c）。
func nextVerifyBackoff(cur, max time.Duration) time.Duration {
	if cur <= 0 {
		return defaultVerifyBackoffMin
	}
	if max <= 0 {
		max = defaultVerifyBackoffMax
	}
	next := cur * 2
	if next > max {
		next = max
	}
	return next
}

// 投影校验的分簇与并发参数（FR-498 P0 优化，2026-10-01）。
const (
	// defaultVerifyChunkEvents 是单次校验查询覆盖的事件上限。
	//
	// 与写入批上限（insertBatchMaxEvents=500）**解耦**：写入批上限保护的是单次插入请求的体积，
	// 而校验查询的成本由「查询窗口内返回的行数」决定，与「本次要求可见的事件数」几乎无关——
	// 分片越细，越多的查询会重复读回同一批行。实测（本地饱和夹具 60 源 ×30 行/s，详见
	// docs/specs/log-verify-chunking/spec.md）：旧实现按 500 条索引切片、各片时间窗相互重叠，
	// 读回量被放大到 k×n（k=片数）；单次校验查询 141ms 里 VL 首字节仅 3.5ms，
	// 其余全是返回体的传输与逐行 JSON 解析。2000 与「单源单次 poll 的典型满批」一致：
	// 常见情形下一批只需 1 次查询。
	// defaultVerifyMaxPerSecond / defaultVerifyMaxInFlight 是校验/对账查询的**全局天花板**默认值
	// （天花板语义：令牌桶容量 = 速率 ⇒ 正常路径的少量查询一次性放行、零等待 ✓）。
	defaultVerifyMaxPerSecond = 30
	defaultVerifyMaxInFlight  = 6
	// defaultVerifyMaxQueriesPerChunk 是**单簇**一次校验的尝试次数上限（从源头降量）。
	//
	// 退避重试原本会一直重试到校验窗口耗尽（生产 5 分钟 ⇒ 单簇 150+ 次查询 ✗）；窗口仍决定
	// "等多久"，本预算决定"最多问几次"，两者取先到者。
	defaultVerifyMaxQueriesPerChunk = 12
	// defaultVerifyRoundBudget 是**每源每轮**的校验时间预算默认值（键
	// log_index.verify.round_budget 可配）。
	//
	// 取值依据（2026-10-03 回退修复实测）：原默认 1.5s 时**整轮预算**被少数源用尽 ⇒ 其余源
	// 每轮 defer（60 源 × 60 行/s 夹具实测 verify 停摆 98%）；调档到 30s 后同口径夹具
	// verify 1108/1108 全成功、事件速率 3255/s（≈基线 3340）、defer=0。
	// 30s 依轮墙钟估算（60 源 / 8 并发 × ~1.2s/源 ≈ 9s）留 ≥3 倍余量；防护仍在（轮墙钟有界）。
	defaultVerifyRoundBudget = 30 * time.Second
	// defaultVerifyChunkBudgetSafety / defaultVerifyChunkBudgetMax 是「单源校验预算随事件数
	// 放大」的安全系数与上限（改动 B；键 log_index.verify.chunk_estimate / chunk_budget_max）。
	//
	// 安全系数取 1.5：单簇估计本就要求「保守取大」，再乘 1.5 是为并发度/限速闸下的排队
	// 留余量（`verifyLimiter` 的 max_in_flight 会让部分簇排在别的源后面）。
	//
	// 上限取 5 分钟（与默认单簇窗口 verificationTimeout 同量级）：现场 272 簇 × 23s ≈ 104
	// 分钟**不可接受**——那会让单源占住校验额度近两小时、轮内其他源被饿死 ✗。
	// 超过上限的部分交给改动 A 的逐轮渐进收敛（这正是 A、B 必须成对落地的原因）。
	// 注意上限**只封顶放大值**：常量预算（round_budget）比它大时以 round_budget 为准 ✓。
	defaultVerifyChunkBudgetSafety = 1.5
	defaultVerifyChunkBudgetMax    = 5 * time.Minute
	defaultVerifyChunkEvents       = 2000
	// defaultVerifyChunkConcurrency 是同批内各校验簇的查询并发度；0/负值取该默认，1 为串行。
	// 校验查询的成本几乎全在「返回体传输 + 客户端逐行 JSON 解析」，属可并行部分；
	// 跨源并发（pollConcurrency）只重叠了源与源之间的等待，源内部的等待仍逐个叠加。
	defaultVerifyChunkConcurrency = 4
	// verifySlowQueryThreshold 是单次校验查询的慢查询告警阈值。旧实现只打「查询开始」，
	// 慢查询在现场不可见（FR-498 定位耗时构成的主要困难之一）。
	verifySlowQueryThreshold = 500 * time.Millisecond
)

// verifyQueryStats 累计一次投影校验的读回量（观测面与回归读数）。
type verifyQueryStats struct {
	Queries int
	Rows    int64
	Bytes   int64
}

// verifyEventTimeKey 返回事件用于分簇排序的时间键；不可解析的时间按零值处理
// （排在最前，彼此相等，保证排序的严格弱序）。
func verifyEventTimeKey(event logtypes.Event) time.Time {
	if when, err := canonicalEventTime(event); err == nil {
		return when
	}
	return time.Time{}
}

// planVerifyChunks 把整批事件按**事件时间**排序后，每 max 条切成一簇。
//
// 为什么按时间切（FR-498 P0）：旧实现按**索引**每 500 条切一片，每片用「本片事件的
// [min-1s, max+1s]」查询。批内事件的时间与索引顺序并不一致（多条流混合、时间戳跳变、
// 跨日批次），各片时间窗因而相互重叠，**同一批的每一行都会被重复读回 k 次**（k=片数）。
// 按时间切簇后，各簇窗口只覆盖自己的时间范围（相邻簇最多在 ±1s 容差处相接），
// 读回总量从 k×n 降到 ≈n：簇内跨度是 min/max 决定的，排序本身就让相邻簇的窗口不再互相包含。
//
// 语义不变：每个事件仍必须在其所属簇的窗口内可见且内容逐字段一致；allowed（整批）的接受规则、
// 重复/多余检测、退避与超时语义全部保持原样（见 verifyProjectionQuery）。
func planVerifyChunks(events []logtypes.Event, max int) [][]logtypes.Event {
	if len(events) == 0 {
		return nil
	}
	if max <= 0 {
		max = defaultVerifyChunkEvents
	}
	ordered := make([]logtypes.Event, len(events))
	copy(ordered, events)
	// 稳定排序：同一时间戳的事件保持原索引顺序，不改变可观察的判定顺序。
	sort.SliceStable(ordered, func(i, j int) bool {
		return verifyEventTimeKey(ordered[i]).Before(verifyEventTimeKey(ordered[j]))
	})
	chunks := make([][]logtypes.Event, 0, (len(ordered)+max-1)/max)
	for start := 0; start < len(ordered); start += max {
		end := start + max
		if end > len(ordered) {
			end = len(ordered)
		}
		chunks = append(chunks, ordered[start:end])
	}
	return chunks
}

// verifyProjection 校验整批事件的投影可见性。
//
// 分批是必须的：VL 对单次查询有内存上限，而恢复期的事件数是整批积压（实测 747,822 条会让 VL
// 返回 400 `cannot calculate [sort by (_time) desc limit ...]`）。但**分批必须让期望集覆盖整批**：
// 各分片的时间范围会重叠（真实事件常共享时间戳），第 N 片的查询会返回其他分片的事件；
// 若只以本片为期望集，这些记录会被误判为 unexpected（2026-09-28 生产实测）。
//
// 分簇按**事件时间**而非索引（见 planVerifyChunks），簇内并发执行（见 verifyChunkConcurrency）。
// 错误语义与串行实现一致：任一簇失败即整体失败，返回**最先观察到**的那个错误，并取消其余簇。
func (m *Manager) verifyProjection(client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) error {
	return m.verifyProjectionCtx(context.Background(), client, source, generation, events)
}

func (m *Manager) verifyProjectionCtx(parent context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) error {
	return m.verifyProjectionCtxYield(parent, client, source, generation, events, nil)
}

// verifyProjectionCtxYield 是校验的**限速形态**：每开始一个校验簇之前调用一次 yield（可为 nil）。
//
// 为什么需要：生产循环里一次重建要校验 **287 个簇**（573562 事件 / 2000 条每簇），
// 这些簇以 `verifyChunkConcurrency`（默认 4）并发跑完；原文一次连续跑完，
// 期间它与采集轮的校验查询争同一条全局天花板（`verifyLimiter`）。簇间让出把这次争抢摊开。
//
// 不变量（安全论证）：yield 只发生在**簇派发之前**，不改变分簇、并发度、退避、
// 错误分类与判定优先级——即它无法让「校验不通过」变成「校验通过」（那是不可能的：
// yield 不接触任何统计量或判定分支）。取消语义同样不变：parent 取消仍在派发前被观察到。
func (m *Manager) verifyProjectionCtxYield(parent context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, yield func()) error {
	_, err := m.verifyProjectionCtxYieldRuns(parent, client, source, generation, events, yield)
	return err
}

// verifyProjectionCtxYieldRuns 是校验的完整形态：在既有**错误语义逐字不变**的前提下，
// 额外返回「本次**逐字段校验通过**的簇所覆盖的源位置连续区间」。
//
// 为什么需要它（2026-10-10 生产活锁修复，改动 A）：大积压单批会被切成数百簇（现场
// `inst:220/file` 544205 事件 / 2000 ⇒ **272 簇**），而单源预算只够跑完其中几簇 ⇒ 整批
// 永远拿不到「全部可见」的结论 ⇒ 不 publish ⇒ 代次不推进 ⇒ 下一轮从头再校验同一批。
// 旧实现里**已经成功的那几簇不留任何痕迹**，于是每一轮都在重复同一段无效功（活锁）。
// 返回已通过区间后，调用方可以把这份**渐进进展**登记进源的持久凭据（VerifiedRuns），
// 使「已校验覆盖」逐轮累积而不是逐轮归零。
//
// 语义红线（务必保持）：
//   - **返回值 err 的判定完全不变**——只有全部簇成功才是 nil；部分成功仍返回错误，
//     错误来源与优先级（semanticErr > otherErr > parent.Err()）逐字与修复前一致。
//   - 返回的区间只是**附带信息**，绝不参与「校验是否通过」的判定。
//   - 只累积 **err == nil 的簇**：失败簇（含被取消、被预算 defer、未能可见的簇）的区间
//     一律不得出现在结果里，否则会把「没校验过」的字节宣称成「已逐字段校验通过」。
func (m *Manager) verifyProjectionCtxYieldRuns(parent context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, yield func(), generationScopes ...[]string) ([]ledger.PositionRange, error) {
	if len(events) == 0 {
		return nil, nil
	}
	// **预算起点后移（2026-10-04，父级裁定 b'）**：单源处理链（全量重写 47 万事件 + 等待）可远超
	// 单源预算；起点停在"源处理开头"时，轮到校验时预算早已耗尽 ⇒ 237 簇只查得动 1 簇（现场
	// queries=0/1 交替）⇒ 整批永不通过 ⇒ 不 publish ⇒ 下轮重写同一批——"events 恒定 × generation
	// 递增"的自锁循环之因。在"即将校验"处重新计时 ⇒ 本批校验拥有完整的单源预算；
	// 轮墙钟上界仍 = ⌈源数/并发⌉ × 预算（有界不破 ✓）。
	//
	// **改动 B（2026-10-10）**：此处同时把**本批事件数**交给预算计算——簇数随积压线性增长
	// （现场 544205 事件 = 272 簇），常量预算下大积压永远跑不完（活锁）。默认仍为常量预算。
	if attempt, _ := parent.Value(verifyAttemptKey{}).(*verifyAttemptBudget); attempt != nil {
		attempt.once.Do(func() {
			attempt.state.deadlineNanos.Store(time.Now().Add(m.verifyBudgetOfEvents(len(events))).UnixNano())
		})
	} else {
		m.setVerifySourceDeadlineFor(source, len(events))
	}
	// 可见性错开（2026-10-03 回退修复 ②）：本函数紧跟在 insert 之后调用 —— VL 的
	// 「写入→可查询」有 ~1.02s 硬地板（本仓实测 1011–1029ms，恒定）。立即探测必然全落在
	// 地板内（0/0.2/0.6s 三次无效探测 + 查询开销全白做）。配了 floor 时先等到地板之后再探测
	// ⇒ 首探即命中。关闭（默认）时行为与本修复前逐字不变 ✓。
	if floor := m.verifyVisibilityFloorOf(); floor > 0 {
		timer := time.NewTimer(floor)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-parent.Done():
			return nil, parent.Err()
		}
	}
	allowed := make(map[string]logtypes.Event, len(events))
	for _, event := range events {
		allowed[event.EventID] = event
	}
	chunks := planVerifyChunks(events, m.verifyChunkEvents)
	concurrency := m.verifyChunkConcurrency
	if concurrency <= 0 {
		concurrency = defaultVerifyChunkConcurrency
	}
	if concurrency > len(chunks) {
		concurrency = len(chunks)
	}
	started := time.Now()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var (
		mu          sync.Mutex
		stats       verifyQueryStats
		semanticErr error
		otherErr    error
		// verifiedRuns 累积「本次**逐字段校验通过**（err == nil）的簇」所覆盖的源位置区间。
		// 由 mu 保护（簇在多个 goroutine 里跑）。语义见 verifyProjectionCtxYieldRuns 的红线。
		verifiedRuns []ledger.PositionRange
	)
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range next {
				chunkStats, err := m.verifyProjectionChunk(ctx, client, source, generation, chunks[idx], allowed, generationScopes...)
				mu.Lock()
				stats.Queries += chunkStats.Queries
				stats.Rows += chunkStats.Rows
				stats.Bytes += chunkStats.Bytes
				if err == nil {
					// 本簇**逐字段校验通过**：把它的覆盖区间记下来（渐进进展的唯一来源）。
					// 失败簇绝不进入这里——那会把「没校验过」的字节宣称成已校验。
					verifiedRuns = append(verifiedRuns, gapRangesOfEvents(chunks[idx])...)
				} else {
					// 判定类错误优先于「被取消的查询」产生的传输类噪音（见 verifyErrorIsSemantic）：
					// 否则首个失败取消其余簇后，现场看到的会是被取消查询的 context canceled /
					// 半行 JSON 解析错误，而真正的根因被掩盖。
					if verifyErrorIsSemantic(err) {
						if semanticErr == nil {
							semanticErr = err
						}
					} else if otherErr == nil {
						otherErr = err
					}
				}
				mu.Unlock()
				if err != nil {
					// 只有携带「明确根因」的失败才取消其余簇（永久失败 / 内容不一致、多余或重复、
					// 事件集非法这类**确定性**判定）：此时收敛是安全的，根因已经拿到。
					//
					// 超时类失败**不取消**：它往往只说明"本批数据尚不可见"，而其余簇此刻可能正读到
					// 真正的根因（例如某一行内容不一致）；取消会把那个结论一并抹掉，让现场只剩
					// 取消噪音（2026-10-01 在 `-race` 下实测到该形态：注入的"写错一行"被
					// `verification failed: context deadline exceeded` 覆盖）。
					//
					// 该策略此前**没有真正生效**：本簇到期唯一的错误文案
					// `not fully visible before deadline` 曾被 verifyErrorIsSemantic 判为判定类，
					// 于是任一簇到期都会走 cancel()（2026-10-02 复审 P1-3 修正，回归见
					// verify_cancel_classification_test.go）。
					if vlsup.IsPermanent(err) || verifyErrorIsSemantic(err) {
						cancel()
					}
				}
			}
		}()
	}
	for i := range chunks {
		if ctx.Err() != nil {
			break // 已有簇失败：不再派发后续簇（与串行实现「失败即返回」一致）
		}
		// 让出点（重建路径）：在派发本簇之前交还时间片。位置在**判定之前**，故它不影响
		// 「谁成功谁失败」；只影响这次校验把多少算力连续压在节点上。
		if i > 0 && yield != nil {
			yield()
		}
		select {
		case next <- i:
		case <-ctx.Done():
		}
	}
	close(next)
	wg.Wait()

	elapsed := time.Since(started)
	mu.Lock()
	finalStats, verdict := stats, semanticErr
	if verdict == nil {
		verdict = otherErr
	}
	if verdict == nil {
		verdict = parent.Err()
	}
	// **成功路径也必须交出区间**（2026-11-02 真机缺陷修复，本函数原注释的那条假设是错的）。
	//
	// 原注释写的是：「全通过时调用方手里已有更强的事实（整批可见），且那条路径已有自己的
	// 区间凭据登记（见 resolveGapsLandedByDelivery）——在这里为几十万事件做一次合并排序纯属浪费。」
	//
	// 实测证明这个假设不成立（单源 15 行/s × 150s，`[DEBT-DIAG]` 观测 + 离线取证）：
	//   · 投影写入结束 354 次，全部 status=200 verified=N —— 写入一直是成功的；
	//   · 投影校验查询 694 次，投影校验完成 0 次（成功路径从不产出区间）；
	//   · 两个调用点（runtime.go:8340、materialization_budget.go:187）都用 `_` 丢弃了返回值；
	//   · 结果 verifiedRanges 长期只有 1 个，而 WAL 里堆积了数百条"未验证"的历史条目。
	//
	// 而 `hasKnownUnpublishedDayDebt` 判的正是**整条 WAL 的历史条目**：
	//
	//	某条旧记录 A 从未进入 verified
	//	  → debt 恒为真
	//	  → 走 debt 分支，登记的是**本次新写入的 B**（runtime.go:7710），不是 A
	//	  → A 依然没登记 ⇒ 下一轮 debt 依然为真 ⇒ **A 永远等不到自己的区间**
	//	  ⇒ closed 被拖着推进（实测停在 150418，而文件已 497879）
	//
	// 铁证：被判为欠账的记录 69716..69933，在 VL 里**已存在 3 份**
	// （projection-3 / projection-15 / projection-17）——数据一份没丢，是记账漏了。
	//
	// 所以成功路径必须交出区间：那条「别处会登记」的路径实际上只登记它自己那批，
	// 没有任何路径回头给历史条目补登记。
	//
	// 成本：`MergePositionRanges` 只对**本次校验的事件**做（不是全源几十万条），
	// 且成功路径本来就是批级操作（已含逐字段查询）。相比"整条链锁死"的代价，这里的合并可忽略。
	passed := ledger.MergePositionRanges(verifiedRuns)
	passedRuns := len(passed)
	mu.Unlock()
	slog.Info("投影校验完成",
		"generation", generation, "source", source.LogSourceID,
		"events", len(events), "chunks", len(chunks), "concurrency", concurrency,
		"queries", finalStats.Queries, "rows", finalStats.Rows, "bytes", finalStats.Bytes,
		"passedRuns", passedRuns,
		"elapsedMs", elapsed.Milliseconds(), "err", errText(verdict))
	return passed, verdict
}

// verifyErrorIsTimeoutLike 判定错误是否只是「本簇自己的校验窗口到期」的产物
// （客户端取消/超时引发的传输类错误），而不是 VL 给出的语义结论。
func verifyErrorIsTimeoutLike(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "context canceled")
}

// verifyErrorIsSemantic 判定错误是否属于「校验自身的**确定性**判定结论」——内容不一致 /
// 多余或重复 / 永久失败 / 事件集非法——而不是「同批其他簇失败后取消」引发的传输类噪音，
// 也不是「本簇窗口到期仍不可见」这类**尚未定型**的结论。
//
// 为什么需要区分（2026-10-01 实测）：同批多簇并发执行时，首个失败会取消其余查询；被取消的
// 请求可能报出 `context canceled`，或把被截断的响应体报成 `unexpected end of JSON input`。
// 若让「最先观察到者胜出」，现场看到的就可能是被取消查询的噪音，真正根因（例如内容不一致）
// 被掩盖。故判定类错误优先返回（串行实现下本来也只会返回判定类错误，语义一致）。
//
// 为什么**不含**「not fully visible before deadline」（2026-10-02 复审 P1-3）：
// 那是本簇自己的窗口到期产生的**非确定性**结论（数据可能只是可见性延迟，退避重试本就是为它
// 准备的），而它恰恰是 chunk 超时时**唯一**的错误文案。一旦把它算作判定类，任一簇到期就会
// `cancel()` 其余簇——另一簇即将读到的真根因（例如内容不一致）被
// `aborted by batch cancellation` 覆盖，现场只剩「不可见 + 取消」的噪音。
// 因此取消只允许由 vlsup.IsPermanent 与真正的判定类错误触发（见 verifyProjection 的取消策略）。
func verifyErrorIsSemantic(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"projection content mismatch",
		"unexpected or duplicate projection event_id",
		"verification failed permanently",
		"duplicate canonical event_id",
		"invalid canonical event time",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

type verifyAttemptKey struct{}

// 每次实际投递owner独享；下一Poll只能产生新待办，不能修改在飞截止/进展。
type verifyAttemptBudget struct {
	once  sync.Once
	state verifySourceBudgetState
}

func (m *Manager) verifyBudgetForContext(ctx context.Context, source SourceConfig, generation string) (time.Duration, error) {
	attempt, _ := ctx.Value(verifyAttemptKey{}).(*verifyAttemptBudget)
	if attempt == nil {
		return m.verifyBudgetFor(source, generation)
	}
	timeout := m.verificationTimeout
	if deadline := attempt.state.deadlineNanos.Load(); deadline > 0 {
		remaining := time.Until(time.Unix(0, deadline))
		if remaining < timeout {
			if attempt.state.progressed.Add(1) == 1 {
				return timeout, nil
			}
			if remaining <= 0 {
				m.verifyDeferred.Add(1)
				return 0, fmt.Errorf("ingest: projection %s %w (attempt budget exhausted)", generation, ErrVerifyDeferred)
			}
			return remaining, nil
		}
	}
	return timeout, nil
}

// verifySourceBudgetState 是单源校验预算的原子状态（per-source，见 Manager.verifySourceBudget）。
type verifySourceBudgetState struct {
	deadlineNanos atomic.Int64 // = now + verifyRoundBudget（unix nanos；0=不限）
	progressed    atomic.Int64 // 本源已推进的校验块数（"每源至少推进一块"的保底判据）
}

// verifySourceState 返回（必要时创建）某源的预算状态。
func (m *Manager) verifySourceState(source SourceConfig) *verifySourceBudgetState {
	key := source.LogSourceID + "/" + source.SourceGeneration
	if v, ok := m.verifySourceBudget.Load(key); ok {
		return v.(*verifySourceBudgetState)
	}
	actual, _ := m.verifySourceBudget.LoadOrStore(key, &verifySourceBudgetState{})
	return actual.(*verifySourceBudgetState)
}

// setVerifySourceDeadline 在**每源处理前**为该源设置独立预算（轮内采集路径调用；
// 每源相互独立 ⇒ 慢源耗尽自身预算不再拖累同轮其他源 ✓）。
//
// 注意：这里用的是**与事件数无关的常量预算**（= 修复前的语义）。真正要校验的那一批事件
// 大小在这一刻尚未确定（要等 poll/deliver 走到校验面），故"随事件数放大"由
// setVerifySourceDeadlineFor 在**即将校验**处覆盖（见 verifyProjectionCtxYieldRuns）。
func (m *Manager) setVerifySourceDeadline(source SourceConfig) {
	m.setVerifySourceDeadlineFor(source, 0)
}

// setVerifySourceDeadlineFor 按**本批事件数**设置该源的校验预算（0 = 用常量预算，
// 即与修复前逐字一致）。
//
// 改动 B（2026-10-10 生产活锁修复）：预算 = max(常量预算, ⌈事件数/簇粒度⌉ × 单簇估计 × 安全系数)，
// 并**必定**被 Max 封顶。理由见 VerifyChunkEstimates；保守默认（PerChunk ≤ 0）下本函数
// 与修复前完全等价（返回常量预算）⇒ 不影响任何现有源。
//
// 上限为何必须存在：无上限时 272 簇 × 23s ≈ 104 分钟会占住单源近两小时的校验额度，
// 采集轮的单源墙钟随之失控（轮内其他源被饿死）。有上限后，超出部分依赖改动 A 的
// 逐轮渐进收敛——这正是两者必须成对落地的原因。
func (m *Manager) setVerifySourceDeadlineFor(source SourceConfig, events int) {
	if m == nil {
		return
	}
	st := m.verifySourceState(source)
	st.deadlineNanos.Store(time.Now().Add(m.verifyBudgetOfEvents(events)).UnixNano())
	st.progressed.Store(0)
}

// verifyBudgetOfEvents 按本批事件数计算单源校验预算（改动 B 的唯一判据处）。
func (m *Manager) verifyBudgetOfEvents(events int) time.Duration {
	base := m.verifyRoundBudgetOf()
	if m == nil || events <= 0 {
		return base
	}
	est := m.verifyChunkEstimatesOf()
	if est.PerChunk <= 0 {
		// 未启用放大：预算与修复前逐字一致（保守默认，绝不影响现有源 ✓）。
		return base
	}
	chunkEvents := m.verifyChunkEvents
	if chunkEvents <= 0 {
		chunkEvents = defaultVerifyChunkEvents
	}
	chunks := (events + chunkEvents - 1) / chunkEvents
	safety := est.Safety
	if safety <= 0 {
		safety = defaultVerifyChunkBudgetSafety
	}
	scaled := time.Duration(float64(est.PerChunk) * float64(chunks) * safety)
	if scaled <= 0 {
		// 溢出保护：折算结果溢出（极端配置）时按上限处理，绝不回落到小预算。
		scaled = time.Duration(1) << 62
	}
	if scaled <= base {
		// 常量预算本来就够：不动（放大只在"常量不够"时才生效 ✓）。
		return base
	}
	max := est.Max
	if max <= 0 {
		max = defaultVerifyChunkBudgetMax
	}
	if scaled > max {
		scaled = max
	}
	if scaled < base {
		// 上限低于运维显式配置的常量预算：常量预算是运维的直接意图，优先于本项放大上限 ✓
		// （上限只用来封顶**放大值**，不回头压低显式配置 ✗）。
		scaled = base
	}
	return scaled
}

// verifyBudgetFor 计算本簇可用的校验超时与"是否必须延期"（把原 verifyProjectionChunk 的
// 内联预算判定抽出，**per-source 优先、轮级兜底**）。
//
// 语义（与 #41 的"轮级 1.5s→30s"衔接）：轮级预算在**深积压长轮**下仍会被轮内前段源吃光
// ⇒ 后段源 queries=0 直接 defer（现场：新进程 verify 13 批 0 成功）。per-source 后，
// **每源都有自己的截止**，互不拖累（消队头阻塞）；"每源至少推进一块"的保底判据同步 per-source 化。
func (m *Manager) verifyBudgetFor(source SourceConfig, generation string) (time.Duration, error) {
	timeout := m.verificationTimeout
	if m == nil {
		return timeout, nil
	}
	// 1) per-source（轮内采集路径优先）
	if v, ok := m.verifySourceBudget.Load(source.LogSourceID + "/" + source.SourceGeneration); ok {
		st := v.(*verifySourceBudgetState)
		if deadlineNanos := st.deadlineNanos.Load(); deadlineNanos > 0 {
			if remaining := time.Until(time.Unix(0, deadlineNanos)); remaining < timeout {
				if st.progressed.Load() == 0 {
					// 保底放行：本源还没完成过任何一块 ⇒ 即使预算已用尽也放行本块 ✓。
					st.progressed.Add(1)
				} else {
					m.verifyDeferred.Add(1)
					if remaining <= 0 {
						return 0, fmt.Errorf("ingest: projection %s %w (round budget exhausted)", generation, ErrVerifyDeferred)
					}
					return remaining, nil
				}
				st.progressed.Add(1)
			}
			return timeout, nil
		}
	}
	// 2) 轮级兜底（启动恢复/对账等非"每源"路径：语义与修复前逐字一致）
	if deadlineNanos := m.verifyRoundDeadline.Load(); deadlineNanos > 0 {
		if remaining := time.Until(time.Unix(0, deadlineNanos)); remaining < timeout {
			if m.verifyRoundProgress.Load() == 0 {
				m.verifyRoundProgress.Add(1)
			} else {
				m.verifyDeferred.Add(1)
				if remaining <= 0 {
					return 0, fmt.Errorf("ingest: projection %s %w (round budget exhausted)", generation, ErrVerifyDeferred)
				}
				return remaining, nil
			}
			m.verifyRoundProgress.Add(1)
		}
	}
	return timeout, nil
}

// verifyProjectionChunk 校验单个簇的事件在 VL 中完全可见（退避重试 + 永久错误立即失败）。
// allowed 是「可接受集合」（整批积压的全部事件）：本簇之外的记录只要属于该集合且内容一致即接受。
// parent 是整批的 ctx：同批其他簇失败/超时会取消它——此时本簇不得把取消噪音（context canceled、
// 被截断响应体的 JSON 解析错误）当成自己的校验结论，否则现场看到的会是噪音而非真正根因。
func (m *Manager) verifyProjectionChunk(parent context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event, generationScopes ...[]string) (verifyQueryStats, error) {
	// 校验预算：**per-source 优先**（消队头阻塞：慢源不再吃掉整轮预算 ⇒ 轮内后段源不再被连坐），
	// 轮级兜底（启动恢复/对账等非"每源"路径语义与修复前逐字一致 ✓）。判定细则见 verifyBudgetFor。
	timeout, budgetErr := m.verifyBudgetForContext(parent, source, generation)
	if budgetErr != nil {
		return verifyQueryStats{}, budgetErr
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// 退避重试（B1c）：原实现以固定 200ms 轮询直至超时（默认 30 秒 ≈ 最多约 150 次校验
	// 查询/源）。高负载时每次查询更慢、重试互相叠加，会把 VL 与磁盘一起压垮——2026-09-28
	// 生产事故的成因之一正是校验查询被如此重试（含 `cannot execute query [...]` 这类 4xx）。
	// 现改为指数退避封顶，并让「重试无意义」的错误（认证失败 / 4xx 语义错误）立即失败。
	backoff := m.verifyBackoffMin
	var lastErr error
	var stats verifyQueryStats
	maxQueries := m.verifyMaxQueriesPerChunk
	if maxQueries <= 0 {
		maxQueries = defaultVerifyMaxQueriesPerChunk
	}
	if shared, _ := parent.Value(publishedPrefixQueryKey{}).(*publishedPrefixQueryBudget); shared != nil {
		maxQueries = min(maxQueries, shared.available())
		if maxQueries <= 0 {
			return stats, ErrVerifyDeferred
		}
	}
	if len(generationScopes) > 0 && len(events) > 0 {
		spans, err := planScopedVerifySpans(source, generationScopes[0], events)
		if err != nil {
			return stats, err
		}
		if len(spans) > 1 {
			window, err := projectionQueryWindowFor(source, events)
			if err != nil {
				return stats, err
			}
			return m.verifyScopedWithReservedBudget(ctx, client, source, generation, events, allowed, spans, window, maxQueries, generationScopes[0])
		}
	}
	for {
		if stats.Queries >= maxQueries {
			// 尝试预算用尽：结论必须与「窗口到期仍不可见」**逐字一致** ✗——绝不返回 nil
			// （那会被当成"校验通过"，是比慢更坏的错 ✗）。措辞沿用下面窗口到期那条。
			if lastErr != nil {
				return stats, fmt.Errorf("ingest: projection %s verification failed: %w", generation, lastErr)
			}
			return stats, fmt.Errorf("ingest: projection %s not fully visible before deadline: %w", generation, context.DeadlineExceeded)
		}
		queryStarted := time.Now()
		complete, queryStats, err := m.verifyProjectionQuery(ctx, client, source, generation, events, allowed, generationScopes...)
		elapsed := time.Since(queryStarted)
		stats.Queries += max(1, queryStats.Queries)
		stats.Rows += queryStats.Rows
		stats.Bytes += queryStats.Bytes
		if elapsed >= verifySlowQueryThreshold {
			// 慢查询单独留痕：读回量（rows/bytes）是判定「窗口内行数过多」还是「VL 慢」的关键。
			slog.Warn("投影校验慢查询",
				"generation", generation, "source", source.LogSourceID,
				"events", len(events), "rows", queryStats.Rows, "bytes", queryStats.Bytes,
				"elapsedMs", elapsed.Milliseconds())
		}
		// 父 ctx 已取消（同批其他簇已拿到明确根因）：本簇的"尚未可见"结论已无意义，立即收手。
		// 注意顺序——先处理本次查询自身的结论，再看取消：判定类错误是本簇的真实结论，
		// 不能被"别的簇先失败"抹掉（见 verifyProjection 的取消策略）。
		if complete && err == nil {
			return stats, nil
		}
		if err != nil {
			if vlsup.IsPermanent(err) {
				return stats, fmt.Errorf("ingest: projection %s verification failed permanently: %w", generation, err)
			}
			// 判定类错误（内容不一致 / 多余或重复 / 事件集非法）是**确定性结论**：VL 里的行内容
			// 已经定型，退避重试不会改变它。旧实现会把它当普通失败继续重试到窗口结束
			// （默认 5 分钟）才报出同一结论——纯浪费，且让现场多等一个窗口。
			// 错误前缀与「重试到窗口结束」路径保持逐字一致，调用方的断言与判读不受影响。
			if verifyErrorIsSemantic(err) {
				return stats, fmt.Errorf("ingest: projection %s verification failed: %w", generation, err)
			}
			if parent.Err() == nil {
				lastErr = err
			} else {
				return stats, fmt.Errorf("ingest: projection %s verification aborted by batch cancellation: %w", generation, parent.Err())
			}
		}
		if parentErr := parent.Err(); parentErr != nil {
			return stats, fmt.Errorf("ingest: projection %s verification aborted by batch cancellation: %w", generation, parentErr)
		}
		select {
		case <-ctx.Done():
			// 本簇窗口到期：根因就是「到期仍不可见」。lastErr 若只是本簇自己的超时引发的传输类
			// 错误（context deadline exceeded / canceled），不改变这一结论——否则现场会把
			// 「数据没可见」误读成「查询失败」（2026-10-01 在 `-race` 下实测到该形态：
			// 注入「少写一行」后错误被改写成 verification failed: context deadline exceeded）。
			if lastErr != nil && !verifyErrorIsTimeoutLike(lastErr) {
				return stats, fmt.Errorf("ingest: projection %s verification failed: %w", generation, lastErr)
			}
			return stats, fmt.Errorf("ingest: projection %s not fully visible before deadline: %w", generation, ctx.Err())
		case <-time.After(backoff):
		}
		backoff = nextVerifyBackoff(backoff, m.verifyBackoffMax)
	}
}

// errText 把可空错误渲染为日志字段（nil → 空串，避免日志里出现 "<nil>"）。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// utcDayBounds 返回 "2006-01-02" 形式的 UTC 日的闭区间 [起, 止]。
// 格式不符时返回 ok=false，调用方保持原行为（向后兼容）。
func utcDayBounds(utcDay string) (time.Time, time.Time, bool) {
	day, err := time.Parse("2006-01-02", utcDay)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return day, day.Add(24*time.Hour - time.Nanosecond), true
}

// sameSourceConfig 判定两次登记是否属于同一份源配置。
//
// UTCDay 是**登记日的派生默认值**（空值填「今天」，见 RegisterSource 归一化），
// 不构成源身份：跨天后同一源的再登记会因它不同而被误判为
// 「another configuration」→ 实例日志采集登记被拒 → 实例起不来
// （2026-10-01 生产实证：午夜后全部重登记被拒，11 台实例无法启动）。
// 故比较前把它归零，其余字段仍严格逐字段比较。
func sameSourceConfig(a, b SourceConfig) bool {
	a.UTCDay, b.UTCDay = "", ""
	return a == b
}

// verifyProjectionQuery 执行一次校验查询，并返回本次的读回量（行数/字节数）。
//
// 与 verifyProjectionOnceWithClient 的唯一差别是 allowed：分批校验时，各簇的时间范围可能相接，
// 查询会返回**同批其他簇**的记录。这些记录必须被接受（否则误判 unexpected），
// 但仍要求内容逐字段一致、且不得重复出现；完整性只要求本簇 events 全部可见。
// allowed 为 nil 时退化为「本簇即全部」的原语义。
//
// 判定规则与旧实现逐字一致（相同的 selector、时间窗、日夹取、逐字段比对与三类错误文案）；
// 新增的 verifyQueryStats 只用于观测与回归读数，不参与任何判定。
func (m *Manager) verifyProjectionQuery(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event, generationScopes ...[]string) (bool, verifyQueryStats, error) {
	window, err := projectionQueryWindowFor(source, events)
	if err != nil {
		return false, verifyQueryStats{}, err
	}
	return m.verifyProjectionQueryInWindow(ctx, client, source, generation, events, allowed, window, generationScopes...)
}

// verifyProjectionQueryInWindow 的全部字节子片共享原簇窗、ctx和许可器，不能靠缩窗弱化重复证据。
// verifyProjectionQueryInWindow 是 verifyProjectionQueryInWindowCore 的薄包装：
// 调用方不需要可见集合时用它，签名与历史保持一致。
func (m *Manager) verifyProjectionQueryInWindow(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event, window projectionVerifyWindow, generationScopes ...[]string) (bool, verifyQueryStats, error) {
	ok, _, stats, err := m.verifyProjectionQueryInWindowCore(ctx, client, source, generation, events, allowed, window, generationScopes...)
	return ok, stats, err
}

// verifyProjectionQueryInWindowCore 执行一次校验查询，并**同时交回可见事件 ID 集合**。
//
// ## 为何交回集合（2026-11-02，真机实测 98.3% 的验证查询是 events=1）
//
// 原先本函数只 `return len(seen) == len(want)`——内部算出的 `seen`
// （VL 返回的每一条可见事件）**被丢弃**。于是调用方 visibleEventsInVL
// 在批不齐时拿不到“哪些可见”，只能把整批逐个重查一遍：
//
//	批次平均 67 条 ⇒ 本应 1 次查询，实际花 67 次
//	实测：总查询 290,727，其中 events=1 占 285,896（98.3%）
//
// 交回集合后，同一批 1 次查询即可得到完整答案。
func (m *Manager) verifyProjectionQueryInWindowCore(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event, window projectionVerifyWindow, generationScopes ...[]string) (bool, map[string]bool, verifyQueryStats, error) {
	var stats verifyQueryStats
	if len(generationScopes) > 0 && len(events) > 0 {
		end, err := scopedProjectionChunkEnd(source, generationScopes[0], events, 0)
		if err != nil {
			return false, nil, stats, err
		}
		if end < len(events) {
			complete := true
			// 子片可见集合必须合并后交出（2026-11-02 定论）。
			//
			// 事故形态（两处叠加）：
			//   ① 第 8906 行原先调的是**薄包装** verifyProjectionQueryInWindow，
			//      子片算出的集合被它内部 `ok, _, stats, err := ...Core(...)` 丢弃；
			//   ② 循环结束只能 `return complete, nil, ...`，集合恒为 nil。
			// 调用方拿到 nil ⇒ visibleSet[id] 恒 false ⇒ 整批全判不可见
			// ⇒ 缺口永不能消解（实测「缺口已消解」恒 0）。
			//
			// 铁证：从被判「VL 查不到」的样本取 eventID 直接查 VL，能查到且代次/时间窗
			// 全对得上；且 missingCount p50=64 与批 events p50=65 近乎 1:1
			// —— 正是「整批一个都没命中」的特征。
			//
			// 为何这是热路径：当累计 selector 超过 scopedProjectionQueryMaxBytes（12KB）
			// 就必分片。固定前缀（代次列表 + log_source_id + source_generation +
			// projectionVerifyFields）本身已占数百字节，60 源常态批就会撞上。
			merged := make(map[string]bool, len(events))
			for start := 0; start < len(events); {
				end, err = scopedProjectionChunkEnd(source, generationScopes[0], events, start)
				if err != nil {
					return false, nil, stats, err
				}
				ok, partSeen, part, queryErr := m.verifyProjectionQueryInWindowCore(ctx, client, source, generation, events[start:end], allowed, window, generationScopes...)
				stats.Queries += part.Queries
				stats.Rows += part.Rows
				stats.Bytes += part.Bytes
				if queryErr != nil {
					return false, nil, stats, queryErr
				}
				for id := range partSeen {
					merged[id] = true
				}
				complete = complete && ok
				start = end
			}
			return complete, merged, stats, nil
		}
	}
	generationSelector := "projection_generation:=" + strconv.Quote(generation)
	if len(generationScopes) > 0 {
		parts := make([]string, 0, len(generationScopes[0]))
		for _, value := range generationScopes[0] {
			parts = append(parts, "projection_generation:="+strconv.Quote(value))
		}
		generationSelector = "(" + strings.Join(parts, " OR ") + ")"
		ids := make([]string, 0, len(events))
		for _, event := range events {
			ids = append(ids, strconv.Quote(event.EventID))
		}
		generationSelector += " AND event_id:in(" + strings.Join(ids, ",") + ")"
	}
	if m.verifyLimiter != nil {
		// 全局天花板（速率 + 在飞）：**正常路径零等待**（桶容量 = 速率 ⇒ 少量查询一次性放行 ✓），
		// 只有风暴被削峰 ✓。采集侧的读/写不走这里 ⇒ 其算力与 VL 容量天然优先 ✓。
		if err := m.verifyLimiter.Acquire(ctx); err != nil {
			return false, nil, stats, err
		}
		defer m.verifyLimiter.Release()
	}
	if client == nil {
		return false, nil, stats, fmt.Errorf("ingest: VictoriaLogs verification client is unavailable")
	}
	want := make(map[string]logtypes.Event, len(events))
	for _, event := range events {
		want[event.EventID] = event
	}
	// 选择器以**事件自身的** source 标识为准，而不是源配置：2026-09-28 生产实测——
	// 配置里的 SourceGeneration 与事件里的 Source.SourceGeneration 指向了不同实例
	// （login-01 的事件被按 beacon-main 的代号查询）→ 查询恒 0 条 → 判「不可见」→
	// 运行时创建失败。校验对象就是「刚写下去的这些事件」，故以它们为准；
	// 配置仅作为事件未携带标识时的兜底。
	sourceID, sourceGeneration := source.LogSourceID, source.SourceGeneration
	if len(events) > 0 {
		if v := events[0].Source.LogSourceID; v != "" {
			sourceID = v
		}
		if v := events[0].Source.SourceGeneration; v != "" {
			sourceGeneration = v
		}
	}
	selector := "projection_generation:=" + strconv.Quote(generation) +
		" AND log_source_id:=" + strconv.Quote(sourceID) +
		" AND source_generation:=" + strconv.Quote(sourceGeneration)
	// 时间窗必须**夹在本源的 UTC 日内**：±1s 的宽容窗会越界到相邻日，
	// 把「上一天批次的日界事件」（如 09-28 窗口下探 1s 落进 09-27T23:59:59.126）
	// 带进结果集；该事件不属于本批 allowed → 误判 `unexpected or duplicate
	// projection event_id` → 运行时创建失败 → 节点离线（2026-09-30 生产事故）。
	// 日界事件在自己那一天的窗口内仍被正常覆盖，完整性不受影响。
	start, end := window.start, window.end
	params := url.Values{
		"query": {selector + " | fields _time, _msg, event_id, level, stream, canonical_content_hash"},
		// 不设 limit：字节子片共享原簇窗口，不能截断跨片时间的重复或内容不一致证据。
		"start": {start.UTC().Format(time.RFC3339Nano)},
		"end":   {end.UTC().Format(time.RFC3339Nano)},
	}
	// 诊断（2026-09-28 生产）：校验恒「不可见」而数据确实在 VL 里时，必须能拿到
	// 平台**实际发出的**查询参数，与手工复刻查询逐字段对照。此前多轮修复都因缺少
	// 这一条日志而在盲改。
	// 遗留发布确认按真实白名单并集读取，不把最近尝试代次误当整日发布面。
	params.Set("query", generationSelector+" AND log_source_id:="+strconv.Quote(sourceID)+" AND source_generation:="+strconv.Quote(sourceGeneration)+projectionVerifyFields)
	if len(generationScopes) > 0 {
		params.Set("query", scopedProjectionQuery(source, generationScopes[0], events))
		if len(params.Get("query")) > scopedProjectionQueryMaxBytes {
			return false, nil, stats, fmt.Errorf("ingest: 逐字段证据query超出字节安全界")
		}
	}
	slog.Info("投影校验查询", "generation", generation, "events", len(events), "queryBytes", len(params.Get("query")), "start", params.Get("start"), "end", params.Get("end"))
	if err := ctx.Err(); err != nil {
		return false, nil, stats, err
	}
	if shared, _ := ctx.Value(publishedPrefixQueryKey{}).(*publishedPrefixQueryBudget); shared != nil && !shared.take() {
		return false, nil, stats, ErrVerifyDeferred
	}
	stats.Queries = 1
	seen := make(map[string]bool, len(events))
	seenOther := make(map[string]bool)
	err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 32<<20+1))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		for scanner.Scan() {
			// 取消（同批其他簇已失败）时立即收手：否则会把被截断的响应体报成
			// `unexpected end of JSON input`，掩盖真正的根因。
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			stats.Rows++
			stats.Bytes += int64(len(scanner.Bytes())) + 1 // +1：NDJSON 行尾换行
			var row struct {
				EventID       string `json:"event_id"`
				CanonicalHash string `json:"canonical_content_hash"`
				EventTimeUTC  string `json:"_time"`
				Message       string `json:"_msg"`
				Level         string `json:"level"`
				Stream        string `json:"stream"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return err
			}
			if proof, _ := ctx.Value(scopedVerifyEvidenceKey{}).(*scopedVerifyEvidence); proof != nil {
				if err := proof.record(row.EventID); err != nil {
					return err
				}
			}
			event, inBatch := want[row.EventID]
			if !inBatch {
				other, ok := allowed[row.EventID]
				if !ok || seenOther[row.EventID] {
					return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
				}
				if row.CanonicalHash != other.CanonicalHash || row.EventTimeUTC != other.EventTimeUTC ||
					row.Message != other.Message || row.Level != other.Level || row.Stream != other.Stream {
					return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
				}
				seenOther[row.EventID] = true
				continue
			}
			if seen[row.EventID] {
				return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
			}
			if row.CanonicalHash != event.CanonicalHash || row.EventTimeUTC != event.EventTimeUTC ||
				row.Message != event.Message || row.Level != event.Level || row.Stream != event.Stream {
				return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
			}
			seen[row.EventID] = true
		}
		return scanner.Err()
	})
	if err != nil {
		return false, nil, stats, err
	}
	return len(seen) == len(want), seen, stats, nil
}

func (m *Manager) verifyProjectionOnceWithClient(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("ingest: VictoriaLogs verification client is unavailable")
	}
	want := make(map[string]logtypes.Event, len(events))
	var first, last time.Time
	for _, event := range events {
		if _, exists := want[event.EventID]; exists {
			return false, fmt.Errorf("duplicate canonical event_id %s", event.EventID)
		}
		when, err := canonicalEventTime(event)
		if err != nil {
			return false, fmt.Errorf("invalid canonical event time for %s: %w", event.EventID, err)
		}
		if first.IsZero() || when.Before(first) {
			first = when
		}
		if last.IsZero() || when.After(last) {
			last = when
		}
		want[event.EventID] = event
	}
	// 选择器以**事件自身的** source 标识为准，而不是源配置：2026-09-28 生产实测——
	// 配置里的 SourceGeneration 与事件里的 Source.SourceGeneration 指向了不同实例
	// （login-01 的事件被按 beacon-main 的代号查询）→ 查询恒 0 条 → 判「不可见」→
	// 运行时创建失败。校验对象就是「刚写下去的这些事件」，故以它们为准；
	// 配置仅作为事件未携带标识时的兜底。
	sourceID, sourceGeneration := source.LogSourceID, source.SourceGeneration
	if len(events) > 0 {
		if v := events[0].Source.LogSourceID; v != "" {
			sourceID = v
		}
		if v := events[0].Source.SourceGeneration; v != "" {
			sourceGeneration = v
		}
	}
	selector := "projection_generation:=" + strconv.Quote(generation) +
		" AND log_source_id:=" + strconv.Quote(sourceID) +
		" AND source_generation:=" + strconv.Quote(sourceGeneration)
	// 时间窗必须**夹在本源的 UTC 日内**：±1s 的宽容窗会越界到相邻日，
	// 把「上一天批次的日界事件」（如 09-28 窗口下探 1s 落进 09-27T23:59:59.126）
	// 带进结果集；该事件不属于本批 allowed → 误判 `unexpected or duplicate
	// projection event_id` → 运行时创建失败 → 节点离线（2026-09-30 生产事故）。
	// 日界事件在自己那一天的窗口内仍被正常覆盖，完整性不受影响。
	start := first.Add(-time.Second)
	end := last.Add(time.Second)
	if dayStart, dayEnd, ok := utcDayBounds(source.UTCDay); ok {
		if start.Before(dayStart) {
			start = dayStart
		}
		if end.After(dayEnd) {
			end = dayEnd
		}
	}
	params := url.Values{
		"query": {selector + " | fields _time, _msg, event_id, level, stream, canonical_content_hash"},
		// 不设 limit：VL 按时间返回窗口内的记录，而分片是按**索引**切的，其时间跨度可能很宽
		// （2026-09-28 生产：500 条事件跨约 10 小时），窗口内除本片外还会有其他分片的记录。
		// 原先 `limit = len(events)+1` 会截断返回，导致本片记录凑不齐 → 误判「不可见」→
		// 运行时创建失败 → 采集静默停摆（多轮盲改后才由参数日志定位）。
		"start": {start.UTC().Format(time.RFC3339Nano)},
		"end":   {end.UTC().Format(time.RFC3339Nano)},
	}
	// 诊断（2026-09-28 生产）：校验恒「不可见」而数据确实在 VL 里时，必须能拿到
	// 平台**实际发出的**查询参数，与手工复刻查询逐字段对照。此前多轮修复都因缺少
	// 这一条日志而在盲改。
	slog.Info("投影校验查询",
		"generation", generation, "events", len(events),
		"query", params.Get("query"), "start", params.Get("start"),
		"end", params.Get("end"), "limit", params.Get("limit"))
	seen := make(map[string]bool, len(events))
	err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 32<<20+1))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		for scanner.Scan() {
			var row struct {
				EventID       string `json:"event_id"`
				CanonicalHash string `json:"canonical_content_hash"`
				EventTimeUTC  string `json:"_time"`
				Message       string `json:"_msg"`
				Level         string `json:"level"`
				Stream        string `json:"stream"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return err
			}
			event, ok := want[row.EventID]
			if !ok || seen[row.EventID] {
				return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
			}
			if row.CanonicalHash != event.CanonicalHash || row.EventTimeUTC != event.EventTimeUTC ||
				row.Message != event.Message || row.Level != event.Level || row.Stream != event.Stream {
				return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
			}
			seen[row.EventID] = true
		}
		return scanner.Err()
	})
	if err != nil {
		return false, err
	}
	return len(seen) == len(want), nil
}

// deliveryConfirmedSegPrefix 是「投递确认补段」的段名前缀（#61-Z-a）：账本里可一眼认出这类段的来历。
const deliveryConfirmedSegPrefix = "delivery-confirmed-"
