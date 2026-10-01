package ingest

// 采集索引（FR-496）的 Manager 侧粘合层：
//   - 内存状态（persistedState）↔ stateindex 行的双向映射；
//   - 旧 ingest.state.json 的一次性迁移：事务写入 → 读回逐字段校验 → 归档；
//     校验不一致时拒绝启动采集（不静默降级），保留旧文件供人工处置；
//   - JSON 只读导出（排障与回滚前的快照）。

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// indexPath 返回采集索引库路径（与旧 ingest.state.json 同级，随 Worker 数据目录迁移）。
func (m *Manager) indexPath() string {
	return filepath.Join(m.root, "var", "log", "ingest.index.db")
}

// openIndex 打开（必要时创建）采集索引库，并在发现旧 JSON 状态时完成一次性迁移。
//
// 任何失败都返回带「拒绝启动采集」字样的错误——由 New() 向上传递，调用方（apps/worker）
// 现网路径会重试后显式 ERROR，绝不静默降级为「不记账继续采集」。
func (m *Manager) openIndex() error {
	store, err := stateindex.Open(m.indexPath())
	if err != nil {
		return fmt.Errorf("ingest: 打开采集索引失败（拒绝启动采集合规）: %w", err)
	}
	m.index = store
	legacy, err := m.readLegacyState()
	if err != nil {
		return err
	}
	if legacy == nil {
		// 无旧 JSON（或为空文件）：直接以索引为准启动。
		return nil
	}
	return m.migrateLegacyState(legacy)
}

// readLegacyState 读取旧 ingest.state.json；不存在或为空文件时返回 nil（视为无旧状态）。
func (m *Manager) readLegacyState() (*persistedState, error) {
	data, err := os.ReadFile(m.statePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ingest: 读取旧采集状态失败（拒绝启动采集）: %w", err)
	}
	if len(data) == 0 {
		return nil, nil // 空文件视为无旧状态（与旧 load() 语义一致）
	}
	var legacy persistedState
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("ingest: 解析旧采集状态失败（拒绝启动采集，保留 %s 供人工处置）: %w", m.statePath, err)
	}
	normalizePersistedState(&legacy)
	return &legacy, nil
}

// migrateLegacyState 把旧 JSON 状态一次性迁入索引库（spec §2.3）：
//   - 空库：事务写入 → 读回逐字段比对 → integrity_check → 旧文件改名归档；
//   - 非空库 + 旧文件仍在：说明上次迁移在「提交后、归档前」中断，或索引被人工改动——
//     逐字段一致则补归档继续启动；不一致则拒绝启动，不静默以任何一侧覆盖另一侧。
func (m *Manager) migrateLegacyState(legacy *persistedState) error {
	desired, err := m.stateToIndexState(legacy)
	if err != nil {
		return fmt.Errorf("ingest: 旧状态无法映射为索引行（拒绝启动采集）: %w", err)
	}
	empty, err := m.index.IsEmpty()
	if err != nil {
		return fmt.Errorf("ingest: 检查索引是否为空失败（拒绝启动采集）: %w", err)
	}
	if !empty {
		loaded, err := m.loadStateFromIndex()
		if err != nil {
			return fmt.Errorf("ingest: 重新读取索引失败（拒绝启动采集）: %w", err)
		}
		if !reflect.DeepEqual(comparisonState(legacy), comparisonState(loaded)) {
			return fmt.Errorf("ingest: 采集索引与旧状态文件不一致，拒绝启动采集（不静默覆盖；保留 %s 与索引库供人工处置）", m.statePath)
		}
		return m.archiveLegacyState()
	}
	if _, err := m.index.Apply(desired); err != nil {
		return fmt.Errorf("ingest: 迁移写入索引失败（拒绝启动采集）: %w", err)
	}
	// 迁移是一次性大事务：写完立刻把 WAL 归并回主库，避免带着 GB 级 WAL 进入稳态
	// （生产演练实测：不归并时 -wal 残留 453.6 MB）。归并失败不改变迁移结果——
	// 数据仍在 WAL 中且可见，降级为「下次打开时自动归并」，故只告警不失败。
	if err := m.index.Checkpoint(); err != nil {
		slog.Warn("采集索引迁移后 WAL 归并未完成（数据仍可见，将延后归并）", "error", err)
	}
	if err := m.index.IntegrityCheck(); err != nil {
		return fmt.Errorf("ingest: 迁移后索引完整性校验失败（拒绝启动采集）: %w", err)
	}
	loaded, err := m.loadStateFromIndex()
	if err != nil {
		return fmt.Errorf("ingest: 迁移后重新读取索引失败（拒绝启动采集）: %w", err)
	}
	if !reflect.DeepEqual(comparisonState(legacy), comparisonState(loaded)) {
		return fmt.Errorf("ingest: 迁移校验失败：索引内容与旧状态不一致，拒绝启动采集（保留 %s 供人工处置）", m.statePath)
	}
	return m.archiveLegacyState()
}

// archiveLegacyState 把旧 JSON 改名归档（保留一个版本，不删除）。
// 归档名带 .migrated 后缀；若已被占用则追加时间戳，绝不覆盖已归档版本。
func (m *Manager) archiveLegacyState() error {
	target := m.statePath + ".migrated"
	if _, err := os.Stat(target); err == nil {
		target = m.statePath + ".migrated." + time.Now().UTC().Format("20060102T150405Z")
	}
	if err := os.Rename(m.statePath, target); err != nil {
		return fmt.Errorf("ingest: 迁移完成但旧状态归档失败（拒绝启动采集）: %w", err)
	}
	return nil
}

// loadStateFromIndex 从索引读出完整状态（与 persistedState 逐字段等价）。
func (m *Manager) loadStateFromIndex() (*persistedState, error) {
	rows, err := m.index.Load()
	if err != nil {
		return nil, err
	}
	if err := stateindex.Validate(rows); err != nil {
		return nil, err
	}
	return indexStateToState(rows)
}

// --- 状态 ↔ 行 映射 -------------------------------------------------------------

// indexAux 是 source_aux.payload 的载体：spec 五张表之外的持久化字段（零行为变更所必需，
// 见 stateindex/schema.go 的 extensionDDL 注释）。SourceConfig 单独落在同行的 config 列。
type indexAux struct {
	PublicationPending bool `json:"publication_pending,omitempty"`
	EventsStored       bool `json:"events_stored,omitempty"`
	// Events 仅旧格式或事件段存储不可用时非空（与 FR-484 语义一致）。
	Events []logtypes.Event `json:"events,omitempty"`

	// LedgerCore 承载「首个账本条目」中未被 position/gap/delivery_batch 表覆盖的字段；
	// 为 nil 表示原 Ledger 切片为空。ExtraLedger 承载首个条目之外的条目（现实中每源恰好一条，
	// 防御性地保留，绝不让数据丢）。
	LedgerCore  *indexLedgerCore `json:"ledger_core,omitempty"`
	ExtraLedger []ledger.Entry   `json:"extra_ledger,omitempty"`
}

// indexLedgerCore 是 ledger.Entry 的残余字段（其余字段分落 spec 各表，逐字段只存一处）。
type indexLedgerCore struct {
	// Key 原样保留账本条目的身份；为空（旧文件缺字段）时回退用 source 表身份。
	Key           ledger.SourceKey        `json:"key,omitempty"`
	Identity      logtypes.SourceIdentity `json:"identity,omitempty"`
	DeliveryPos   uint64                  `json:"delivery_pos,omitempty"`
	DeliveryState logtypes.DeliveryState  `json:"delivery_state,omitempty"`
	Segments      []ledger.Segment        `json:"segments,omitempty"`
	Rotations     []ledger.RotationLink   `json:"rotations,omitempty"`
	RecoveryRefs  []ledger.RecoveryRef    `json:"recovery_refs,omitempty"`
	ErrorCount    int                     `json:"error_count,omitempty"`
	IngestSeq     uint64                  `json:"ingest_seq,omitempty"`
}

// splitSourceKey 从组合键回推 LogSourceID/SourceGeneration（键形如 `<log_id>/<generation>`，
// log_id 自身可含 "/"，故只切最后一段；仅当配置与账本都提供不了身份时用）。
func splitSourceKey(key string) (string, string) {
	index := stringsLastIndexByte(key, '/')
	if index < 0 {
		return key, ""
	}
	return key[:index], key[index+1:]
}

func stringsLastIndexByte(text string, b byte) int {
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == b {
			return i
		}
	}
	return -1
}

// stateToIndexState 把内存状态映射为索引期望行（只包含非空键的行；行序无关，Apply 按指纹差异写入）。
func (m *Manager) stateToIndexState(st *persistedState) (stateindex.State, error) {
	var out stateindex.State
	keys := make(map[string]struct{}, len(st.Sources)+len(st.SourceConfigs))
	for key := range st.Sources {
		keys[key] = struct{}{}
	}
	for key := range st.SourceConfigs {
		keys[key] = struct{}{}
	}
	sortedKeys := make([]string, 0, len(keys))
	for key := range keys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)

	for _, key := range sortedKeys {
		config, hasConfig := st.SourceConfigs[key]
		saved, hasState := st.Sources[key]

		logID, generation := "", ""
		if hasConfig {
			logID, generation = config.LogSourceID, config.SourceGeneration
		}
		var core *indexLedgerCore
		if hasState {
			if len(saved.Ledger) > 0 {
				core = ledgerCoreOf(&saved.Ledger[0])
			}
			if logID == "" && core != nil {
				logID, generation = core.Key.LogSourceID, core.Key.SourceGeneration
			}
		}
		if logID == "" {
			logID, generation = splitSourceKey(key)
		}
		namespace := ""
		if hasConfig {
			namespace = config.StorageNamespace
		}
		out.Sources = append(out.Sources, stateindex.SourceRow{
			Key: key, LogSourceID: logID, SourceGeneration: generation, StorageNamespace: namespace,
		})
		var aux indexAux
		var configJSON []byte
		var marshalErr error
		if config, ok := st.SourceConfigs[key]; ok {
			configJSON, marshalErr = json.Marshal(&config)
			if marshalErr != nil {
				return stateindex.State{}, fmt.Errorf("ingest: 编码 %s 的配置失败: %w", key, marshalErr)
			}
		}
		if hasState {
			aux.PublicationPending = saved.PublicationPending
			aux.EventsStored = saved.EventsStored
			aux.Events = saved.Events
			aux.LedgerCore = core
			if len(saved.Ledger) > 1 {
				// 防御：首个条目之外的账本条目原样保留（现实中每源恰好一条）。
				aux.ExtraLedger = saved.Ledger[1:]
			}
			position, projection, gaps, batches, err := ledgerRowsOf(key, saved, core)
			if err != nil {
				return stateindex.State{}, err
			}
			out.Positions = append(out.Positions, position)
			if projection != nil {
				out.Projections = append(out.Projections, *projection)
			}
			out.Gaps = append(out.Gaps, gaps...)
			out.Batches = append(out.Batches, batches...)
			inline, err := m.walRowsOf(key, saved)
			if err != nil {
				return stateindex.State{}, err
			}
			out.WAL = append(out.WAL, inline...)
		}
		auxConfig, err := json.Marshal(&aux)
		if err != nil {
			return stateindex.State{}, fmt.Errorf("ingest: 编码 %s 的附属数据失败: %w", key, err)
		}
		out.Aux = append(out.Aux, stateindex.AuxRow{Key: key, Config: configJSON, Payload: auxConfig})
	}

	for uuid, binding := range st.Instances {
		out.Instances = append(out.Instances, stateindex.InstanceRow{
			UUID: uuid, Namespace: binding.TargetID, Generation: binding.Generation,
			Mode: string(binding.Mode), WorkDir: binding.WorkDir,
		})
	}
	return out, nil
}

// ledgerCoreOf 从账本条目提取残余字段。
func ledgerCoreOf(entry *ledger.Entry) *indexLedgerCore {
	return &indexLedgerCore{
		Key: entry.Key, Identity: entry.Identity, DeliveryPos: entry.Positions.Delivery,
		DeliveryState: entry.DeliveryState, Segments: entry.Segments, Rotations: entry.Rotations,
		RecoveryRefs: entry.RecoveryRefs, ErrorCount: entry.ErrorCount, IngestSeq: entry.IngestSeq,
	}
}

// stateToIndexStateScoped 是 stateToIndexState 的增量形态：账本派生行（gap/source_wal/
// delivery_batch）只按 changed 里的源构建，根行（source/position/projection/source_aux/
// instance_binding）恒为全量。
//
// 为什么根行恒全量：它们的行数是 O(源数)（几百行级别），全量构建+索引层全量比对每轮只花
// 毫秒级；而它们承载投影代次/发布待定/实例绑定等由 Manager 直接改 state 的字段，
// 全量比对让这些字段的变更无需任何显式标记即正确落库。
func (m *Manager) stateToIndexStateScoped(st *persistedState, changed []string) (stateindex.State, error) {
	// 先按 changed 构建账本派生行（该函数只遍历给定源）。
	scoped := &persistedState{Sources: make(map[string]persistedSource, len(changed))}
	for _, key := range changed {
		if saved, ok := st.Sources[key]; ok {
			scoped.Sources[key] = saved
		}
	}
	partial, err := m.stateToIndexState(scoped)
	if err != nil {
		return stateindex.State{}, err
	}
	// 再补上全部根行：先全量构建再覆盖派生行（source_aux 也是根，直接全量）。
	full, err := m.stateToIndexState(st)
	if err != nil {
		return stateindex.State{}, err
	}
	full.Gaps = partial.Gaps
	full.WAL = partial.WAL
	full.Batches = partial.Batches
	// source/position/projection/source_aux/instances 都在 full 里。
	return full, nil
}

// ledgerRowsOf 把「首个账本条目」拆成 position/gap/delivery_batch 行（spec §2.2 各表只存一处）。
func ledgerRowsOf(key string, saved persistedSource, core *indexLedgerCore) (stateindex.PositionRow, *stateindex.ProjectionRow, []stateindex.GapRow, []stateindex.BatchRow, error) {
	position := stateindex.PositionRow{Key: key}
	projection := &stateindex.ProjectionRow{
		Key: key, Generation: saved.ProjectionGeneration, EventsStoredThrough: saved.EventsStoredThrough,
		Pending: saved.PublicationPending,
	}
	if len(saved.Ledger) == 0 {
		return position, projection, nil, nil, nil
	}
	entry := saved.Ledger[0]
	position.ReadPos = entry.Positions.Read
	position.DurablePos = entry.Positions.Durable
	position.ReclaimPos = entry.Positions.Reclaim
	position.AcquirePaused = entry.AcquirePaused
	position.PauseReason = entry.PauseReason
	gaps := make([]stateindex.GapRow, 0, len(entry.Gaps))
	for index, gap := range entry.Gaps {
		gaps = append(gaps, stateindex.GapRow{
			ID: int64(index), Key: key,
			StartPos: gap.StartPos, EndPos: gap.EndPos, Reason: gap.Reason, Detail: gap.Detail,
			Resolved: gap.Resolved, Resolution: gap.Resolution,
		})
	}
	batches := make([]stateindex.BatchRow, 0, len(entry.DeliveryBatches))
	for index, batch := range entry.DeliveryBatches {
		batches = append(batches, stateindex.BatchRow{
			Key: key, Ordinal: int64(index), StartPos: batch.Start, EndPos: batch.End, State: string(batch.State),
		})
	}
	return position, projection, gaps, batches, nil
}

// walRowsOf 把该源的 WAL 条目录入 source_wal 行：正文内联条目带 Body，引用条目 Body 为 nil。
// FP 覆盖正文的每一个 JSON 字段（见 walBodyFP），未变更的条目不会触发正文重编码。
func (m *Manager) walRowsOf(key string, saved persistedSource) ([]stateindex.WALRow, error) {
	rows := make([]stateindex.WALRow, 0, len(saved.WAL)+len(saved.WALRefs))
	for _, entry := range saved.WAL {
		body, err := json.Marshal(&entry.Event)
		if err != nil {
			return nil, fmt.Errorf("ingest: 编码 %s 的 WAL 事件失败: %w", key, err)
		}
		rows = append(rows, stateindex.WALRow{
			Key: key, Seq: entry.Seq, EventID: entry.Event.EventID,
			RecordStart: entry.Event.Record.Start, RecordEnd: entry.Event.Record.End,
			Appended: entry.Appended, Durable: entry.Durable, Body: body,
			FP: walBodyFP(entry.Event),
		})
	}
	for _, ref := range saved.WALRefs {
		rows = append(rows, stateindex.WALRow{
			Key: key, Seq: ref.Seq, EventID: ref.EventID,
			RecordStart: ref.RecordStart, RecordEnd: ref.RecordEnd,
			Appended: ref.Appended, Durable: ref.Durable, Body: nil,
			FP: uint64(0), // 引用条目的正文在事件段存储中，身份字段即判据
		})
	}
	return rows, nil
}

// walBodyFP 计算 WAL 正文的变更判据，覆盖 logtypes.Event 落库 JSON 的**全部**字段：
//
//	event_id、source{log_source_id, source_generation, parser_version}、record{start,end}、
//	event_time_utc、ingest_time_utc、level、stream、message、canonical_content_hash、
//	fields（按 key 排序后逐项）
//
// 每个字段都按**自身取值**参与，不依赖任何派生关系（曾只依赖 event_id 覆盖 source/record，
// 但若 source 被改动而 event_id 未同步，正文会变而指纹不变 → 静默留下过期正文；
// TestWALBodyFingerprintCoversEveryEventField 正是用单点变异守住这条）。
//
// 因此「同一 (身份) 下 FP 相同 ⇒ 正文相同」成立（职责见 stateindex.WALFingerprint 的注释）。
func walBodyFP(ev logtypes.Event) uint64 {
	hash := fnv.New64a()
	writeString := func(text string) {
		var length [8]byte
		n := uint64(len(text))
		for i := 0; i < 8; i++ {
			length[i] = byte(n >> (8 * i))
		}
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(text))
	}
	writeUint := func(value uint64) {
		var buf [8]byte
		for i := 0; i < 8; i++ {
			buf[i] = byte(value >> (8 * i))
		}
		_, _ = hash.Write(buf[:])
	}
	writeString("v2") // 指纹版本：口径变化必须整体改变指纹，避免旧值被误判为未变更
	writeString(ev.EventID)
	writeString(ev.Source.LogSourceID)
	writeString(ev.Source.SourceGeneration)
	writeString(ev.Source.ParserVersion)
	writeUint(ev.Record.Start)
	writeUint(ev.Record.End)
	writeString(ev.EventTimeUTC)
	writeString(ev.IngestTimeUTC)
	writeString(ev.Level)
	writeString(ev.Stream)
	writeString(ev.Message)
	writeString(ev.CanonicalHash)
	keys := make([]string, 0, len(ev.Fields))
	for key := range ev.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	writeUint(uint64(len(keys)))
	for _, key := range keys {
		writeString(key)
		writeString(ev.Fields[key])
	}
	return hash.Sum64()
}

// indexStateToState 把索引行还原为内存状态（与 stateToIndexState 互为逆映射）。
func indexStateToState(rows stateindex.State) (*persistedState, error) {
	st := &persistedState{
		Sources:       make(map[string]persistedSource),
		SourceConfigs: make(map[string]SourceConfig),
		Instances:     make(map[string]InstanceBinding),
	}
	positions := make(map[string]stateindex.PositionRow, len(rows.Positions))
	for _, row := range rows.Positions {
		positions[row.Key] = row
	}
	gaps := make(map[string][]ledger.Gap, len(rows.Gaps))
	for _, row := range rows.Gaps {
		gaps[row.Key] = append(gaps[row.Key], ledger.Gap{
			StartPos: row.StartPos, EndPos: row.EndPos, Reason: row.Reason, Detail: row.Detail,
			Resolved: row.Resolved, Resolution: row.Resolution,
		})
	}
	batches := make(map[string][]ledger.DeliveryBatch, len(rows.Batches))
	for _, row := range rows.Batches {
		batches[row.Key] = append(batches[row.Key], ledger.DeliveryBatch{
			Start: row.StartPos, End: row.EndPos, State: logtypes.DeliveryState(row.State),
		})
	}
	projections := make(map[string]stateindex.ProjectionRow, len(rows.Projections))
	for _, row := range rows.Projections {
		projections[row.Key] = row
	}
	auxMap := make(map[string]stateindex.AuxRow, len(rows.Aux))
	for _, row := range rows.Aux {
		auxMap[row.Key] = row
	}
	walInline := make(map[string][]acquire.WALEntry)
	walRefs := make(map[string][]acquire.WALRef)
	for _, row := range rows.WAL {
		if row.Body == nil {
			walRefs[row.Key] = append(walRefs[row.Key], acquire.WALRef{
				Seq: row.Seq, Appended: row.Appended, Durable: row.Durable,
				EventID: row.EventID, RecordStart: row.RecordStart, RecordEnd: row.RecordEnd,
			})
			continue
		}
		var event logtypes.Event
		if err := json.Unmarshal(row.Body, &event); err != nil {
			return nil, fmt.Errorf("ingest: 解析 %s 的 WAL 正文失败: %w", row.Key, err)
		}
		walInline[row.Key] = append(walInline[row.Key], acquire.WALEntry{
			Seq: row.Seq, Event: event, Appended: row.Appended, Durable: row.Durable,
		})
	}

	for _, row := range rows.Sources {
		key := row.Key
		if auxRow, ok := auxMap[key]; ok && len(auxRow.Config) > 0 {
			var cfg SourceConfig
			if err := json.Unmarshal(auxRow.Config, &cfg); err != nil {
				return nil, fmt.Errorf("ingest: 解析 %s 的配置失败: %w", key, err)
			}
			st.SourceConfigs[key] = cfg
		}
		position, hasState := positions[key]
		if !hasState {
			continue // 只有配置、没有状态（position 行存在 ⟺ Sources 有该键）
		}
		var aux indexAux
		if auxRow, ok := auxMap[key]; ok && len(auxRow.Payload) > 0 {
			if err := json.Unmarshal(auxRow.Payload, &aux); err != nil {
				return nil, fmt.Errorf("ingest: 解析 %s 的附属数据失败: %w", key, err)
			}
		}
		var ledgerEntries []ledger.Entry
		if aux.LedgerCore != nil {
			core := aux.LedgerCore
			entry := ledger.Entry{
				Key:      core.Key,
				Identity: core.Identity,
				Positions: logtypes.Positions{
					Read: position.ReadPos, Durable: position.DurablePos,
					Delivery: core.DeliveryPos, Reclaim: position.ReclaimPos,
				},
				DeliveryState:   core.DeliveryState,
				DeliveryBatches: batches[key],
				Segments:        core.Segments,
				Rotations:       core.Rotations,
				Gaps:            gaps[key],
				RecoveryRefs:    core.RecoveryRefs,
				ErrorCount:      core.ErrorCount,
				IngestSeq:       core.IngestSeq,
				AcquirePaused:   position.AcquirePaused,
				PauseReason:     position.PauseReason,
			}
			if entry.Key.LogSourceID == "" || entry.Key.SourceGeneration == "" {
				entry.Key = ledger.SourceKey{LogSourceID: row.LogSourceID, SourceGeneration: row.SourceGeneration}
			}
			ledgerEntries = append(ledgerEntries, entry)
		}
		ledgerEntries = append(ledgerEntries, aux.ExtraLedger...)
		projection := projections[key]
		st.Sources[key] = persistedSource{
			PublicationPending:   aux.PublicationPending,
			Ledger:               ledgerEntries,
			WAL:                  walInline[key],
			WALRefs:              walRefs[key],
			EventsStoredThrough:  projection.EventsStoredThrough,
			ProjectionGeneration: projection.Generation,
			EventsStored:         aux.EventsStored,
			Events:               aux.Events,
		}
	}

	for _, row := range rows.Instances {
		st.Instances[row.UUID] = InstanceBinding{
			UUID: row.UUID, TargetID: row.Namespace, Generation: row.Generation,
			Mode: pipeline.AcquireMode(row.Mode), WorkDir: row.WorkDir,
		}
	}
	normalizePersistedState(st)
	return st, nil
}

// normalizePersistedState 把状态归一化，使「对比两侧」与「重建结果」在同一形态下可比：
//   - nil 切片/映射 → 空（读取路径只依赖 len，二者行为等价；DB 表无法区分 nil 与空）；
//   - WAL / WALRefs 按 (seq, 事件身份) 排序（与 acquire.RestoreMixed 的恢复语义一致）。
//
// 注意：派生字段 Positions.Delivery **不在此处重算**——落库保留原值，只在比对时统一口径
// （见 comparisonState）。这样「字段是否被搬运」仍可被断言到。
func normalizePersistedState(st *persistedState) {
	if st.Sources == nil {
		st.Sources = make(map[string]persistedSource)
	}
	if st.SourceConfigs == nil {
		st.SourceConfigs = make(map[string]SourceConfig)
	}
	if st.Instances == nil {
		st.Instances = make(map[string]InstanceBinding)
	}
	for key, saved := range st.Sources {
		if saved.Ledger == nil {
			saved.Ledger = []ledger.Entry{}
		}
		for index := range saved.Ledger {
			entry := &saved.Ledger[index]
			if entry.Gaps == nil {
				entry.Gaps = []ledger.Gap{}
			}
			if entry.DeliveryBatches == nil {
				entry.DeliveryBatches = []ledger.DeliveryBatch{}
			}
			if entry.Segments == nil {
				entry.Segments = []ledger.Segment{}
			}
			if entry.Rotations == nil {
				entry.Rotations = []ledger.RotationLink{}
			}
			if entry.RecoveryRefs == nil {
				entry.RecoveryRefs = []ledger.RecoveryRef{}
			}
		}
		if saved.WAL == nil {
			saved.WAL = []acquire.WALEntry{}
		}
		if saved.WALRefs == nil {
			saved.WALRefs = []acquire.WALRef{}
		}
		if saved.Events == nil {
			saved.Events = []logtypes.Event{}
		}
		sort.SliceStable(saved.WAL, func(i, j int) bool { return lessWALEntry(saved.WAL[i], saved.WAL[j]) })
		sort.SliceStable(saved.WALRefs, func(i, j int) bool { return lessWALRef(saved.WALRefs[i], saved.WALRefs[j]) })
		st.Sources[key] = saved
	}
}

func lessWALEntry(a, b acquire.WALEntry) bool {
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	if a.Event.EventID != b.Event.EventID {
		return a.Event.EventID < b.Event.EventID
	}
	if a.Event.Record.Start != b.Event.Record.Start {
		return a.Event.Record.Start < b.Event.Record.Start
	}
	return a.Event.Record.End < b.Event.Record.End
}

func lessWALRef(a, b acquire.WALRef) bool {
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	if a.EventID != b.EventID {
		return a.EventID < b.EventID
	}
	if a.RecordStart != b.RecordStart {
		return a.RecordStart < b.RecordStart
	}
	return a.RecordEnd < b.RecordEnd
}

// comparisonState 返回「比对口径」下的状态：在 normalizePersistedState 之上，把派生字段
// Positions.Delivery 按 ledger 的口径重算（ledger.Restore 本就重算它，见
// ledger.ContiguousDeliveryEnd），并按**同一判据**裁掉水位之下的历史投递批次。
//
// 为什么只在比对时重算：索引落库保留原值，便于断言字段确实被搬运；而比对必须忽略派生值噪声
// ——真机现场是「归档 JSON 里是写回前的旧值、索引里是重算后的值」，二者语义相同，按原值比对
// 会把一次正常启动误判为校验失败并拒绝采集。
//
// 为什么还要在比对里裁剪：裁剪只发生在**写路径**上（ledger 的写方法），而归档 JSON 与索引库的
// 裁剪进度可以不同——真机可达的形态是「迁移已在库中提交、归档尚未完成」（migrateLegacyState
// 的那条分支），此时旧 JSON 仍是完整历史、索引里已被裁过。按原样 DeepEqual 会把一次正常启动
// 误判为「索引与旧状态不一致」而拒绝启动采集。裁剪是恒等元变换（判据与证明见
// ledger.PruneDeliveryBatches），归一后两侧仍逐字段可比，真正的搬运错误照样会被抓出来。
//
// 口径固定为「严格按水位」（keepRecent=0）：两侧同口径即可，开关只影响落库行数、不影响语义。
func comparisonState(st *persistedState) *persistedState {
	normalizePersistedState(st)
	for key, saved := range st.Sources {
		for index := range saved.Ledger {
			entry := &saved.Ledger[index]
			entry.DeliveryBatches = prunedBatchesForComparison(entry)
			entry.Positions.Delivery = ledger.ContiguousDeliveryEnd(entry.DeliveryBatches, entry.Positions.Reclaim)
		}
		st.Sources[key] = saved
	}
	return st
}

// prunedBatchesForComparison 把条目的投递批次按水位归一（失败即保留原列表，不新增失败面）。
//
// 它同时保证了「裁空」与「本来为空」在同一形态下可比：PruneDeliveryBatches 在裁空时返回
// 非 nil 的空切片（与 normalizePersistedState 的归一形态一致）。
func prunedBatchesForComparison(entry *ledger.Entry) []ledger.DeliveryBatch {
	kept, dropped, err := ledger.PruneDeliveryBatches(entry.DeliveryBatches, entry.Positions.Reclaim, 0)
	if err != nil {
		slog.Warn("迁移校验前的投递批次归一被守卫拦下，按完整历史比对（不影响采集正确性）",
			"logSourceID", entry.Key.LogSourceID, "generation", entry.Key.SourceGeneration,
			"reclaim", entry.Positions.Reclaim, "batches", len(entry.DeliveryBatches), "error", err)
		return entry.DeliveryBatches
	}
	if dropped == 0 {
		return entry.DeliveryBatches
	}
	return kept
}

// indexPruneConfigOf 归一化「历史投递批次裁剪」配置：nil 表示用默认（开启、严格按水位）。
// 非法值（负数尾窗）在 ledger.Normalized 里回退默认——配置误写不得放宽判据。
func indexPruneConfigOf(configured *ledger.DeliveryBatchPruneConfig) ledger.DeliveryBatchPruneConfig {
	if configured == nil {
		return ledger.DefaultDeliveryBatchPruneConfig()
	}
	return configured.Normalized()
}

// PersistSamples 返回最近一次持久化的采样（供测试与真机观测「空闲不重写历史」与 P50/P95）。
func (m *Manager) PersistSamples() []stateindex.Sample {
	if m == nil || m.index == nil {
		return nil
	}
	return m.index.Samples()
}

// PersistLatency 返回持久化耗时采样环上的分位（真机验收「单次持久化 ≤50ms」的读数，spec §3.3）。
//
// 注意：采样环随索引句柄存在，Stop/CloseIndex 之后即被清空——需在停止**之前**读数。
func (m *Manager) PersistLatency() stateindex.Latency {
	if m == nil || m.index == nil {
		return stateindex.Latency{}
	}
	return m.index.Latency()
}

// PersistNow 立即执行一次持久化并把本次写入统计返回给调用方。
//
// 用途：真机/演练取「单次持久化」的现场读数——验收判据是「60 源下单次持久化 ≤50ms」，
// 需要能主动驱动一次并直接读到本次的行数/字节/耗时，而不是只能等轮询恰好触发。
// 语义与轮询路径完全一致（同一 persist），因此读到的就是生产口径。
func (m *Manager) PersistNow() (stateindex.Stats, error) {
	if m == nil {
		return stateindex.Stats{}, nil
	}
	if err := m.persist(); err != nil {
		return stateindex.Stats{}, err
	}
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()
	if index == nil {
		return stateindex.Stats{}, nil
	}
	samples := index.Samples()
	if len(samples) == 0 {
		return stateindex.Stats{}, nil
	}
	last := samples[len(samples)-1]
	return stateindex.Stats{
		RowsWritten: last.RowsWritten, RowsDeleted: last.RowsDeleted,
		RowsPlanned: last.RowsPlanned, BytesWritten: last.BytesWritten, Duration: last.Duration,
	}, nil
}

// ExportIndexJSON 只读导出索引内容为旧 ingest.state.json 同等结构的 JSON（排障与回滚参考）。
// 库不存在或不可读时返回错误；不创建、不修改任何文件。
func ExportIndexJSON(root string, out io.Writer) error {
	if root == "" {
		return fmt.Errorf("ingest: 导出需要数据根目录")
	}
	store, err := stateindex.OpenReadOnly(filepath.Join(root, "var", "log", "ingest.index.db"))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	rows, err := store.Load()
	if err != nil {
		return fmt.Errorf("ingest: 读取索引失败: %w", err)
	}
	state, err := indexStateToState(rows)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(state)
}
