package ingest

// 采集索引（FR-496）的 Manager 侧粘合层：
//   - 内存状态（persistedState）↔ stateindex 行的双向映射；
//   - JSON 只读导出（排障与回滚前的快照）。

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// indexPath 返回采集索引库路径。
func (m *Manager) indexPath() string {
	return filepath.Join(m.root, "var", "log", "ingest.index.db")
}

// openIndex 打开（必要时创建）采集索引库。
//
// 任何失败都返回带「拒绝启动采集」字样的错误——由 New() 向上传递，调用方（apps/worker）
// 现网路径会重试后显式 ERROR，绝不静默降级为「不记账继续采集」。
func (m *Manager) openIndex() error {
	store, err := stateindex.OpenWithBudget(m.indexPath(), m.indexCommit)
	if err != nil {
		return fmt.Errorf("ingest: 打开采集索引失败（拒绝启动采集）: %w", err)
	}
	m.index = store
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
	// VerifiedRuns 是该源逐字段校验通过的源位置连续区间（见 persistedSource.VerifiedRuns）。
	// 直接随本 payload 的 JSON 落盘：payload 列的指纹覆盖其全部字节，故变更必然被索引层发现。
	VerifiedRuns []ledger.PositionRange `json:"verified_runs,omitempty"`
	// IdentityConflicts 是不可裁定身份冲突的隔离记录，随附属 payload 持久化。
	IdentityConflicts []identityConflictRecord `json:"identity_conflicts,omitempty"`
	// IdentityConflictClearances 是「人工确认并清空身份冲突」的审计凭据（见
	// persistedSource.IdentityConflictClearances）。与 IdentityConflicts 并存于同一 payload：
	// 清空的凭据与它描述的那份记录同生共死，不引入跨结构一致性要求。
	IdentityConflictClearances []identityConflictClearance `json:"identity_conflict_clearances,omitempty"`
	// Abandonments 是「人工裁定永久丢失」的区间凭据（见 ledger.Entry.Abandonments）。
	//
	// 为什么必须落在本 payload 里（后续项③附带修复，2026-10-02）：ADR-101 要求该凭据
	// **比缺口记录活得久**（已解决缺口会被 trimResolvedGapsLocked 裁掉，凭据要一直支撑
	// 回收链的放行判定与查询面的"永久缺失"标记），但此前它既不在 spec 五表里、也不在本
	// payload 里——重启后凭据全部丢失，`AbandonedRanges` 查询面标记与 `CanReclaim` 的
	// 凭据放行会一并静默消失。放在本 payload（而不是新增表）是刻意的：JSON 字节即列指纹，
	// 变更必然被索引层发现，无需迁移 DDL，与 VerifiedRuns 同一形态。
	Abandonments []ledger.GapAbandonment `json:"abandonments,omitempty"`

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
	ErrorCount    int                     `json:"error_count,omitempty"`
	IngestSeq     uint64                  `json:"ingest_seq,omitempty"`
	// PositionsFingerprint / PositionsFingerprintAt 是「已确认水位处」的内容采样指纹与它对应的位置
	// （语义见 ledger.Entry 同名字段）。
	//
	// 为什么必须落在本 payload 里（w18 漂移防护的持久化断层修复，2026-10-17）：生产持久化走的是
	// 本索引（ledgerCoreOf + indexStateToState），而 w18 的指纹此前只被
	// ledger.Snapshot()/Restore() 搬运——那对方法只有测试在用。于是进程重启或代次补账
	// （runtime.go 经 saved.Ledger → ledger.Restore 还原）后指纹恒为零值，段 3（acquire 续读前
	// 校验）按设计把「缺指纹」判为不一致 ⇒ **每次重启都会走对账补齐、禁止按旧偏移续读**，
	// 恰好是该指纹要消除的「漂移重复摄取」。指纹丢了，防护就等于没装。
	//
	// JSON tag 与 ledger.Entry 逐字一致（含 omitempty）：默认零值时不出现在 payload 里，
	// 于是**从未采样过指纹的源**在升级前后的 payload 字节完全相同——索引按 JSON 字节判变更，
	// 不做无谓的全量重写；而旧索引缺这两个字段时反序列化得到零值（= 既有的「缺指纹」语义，
	// 段 3 本就把零值当不一致），因此旧库只读不报错、不触发任何异常路径，由下一轮采样自然补齐。
	PositionsFingerprint   string                       `json:"positions_fingerprint,omitempty"`
	PositionsFingerprintAt uint64                       `json:"positions_fingerprint_at,omitempty"`
	ConfirmedFingerprint   *ledger.ConfirmedFingerprint `json:"confirmed_fingerprint,omitempty"`
	RequestDoneRanges      []ledger.PositionRange       `json:"request_done_ranges,omitempty"`
	DeliveryEvidence       []ledger.DeliveryBatch       `json:"delivery_evidence,omitempty"`
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
	return m.stateToIndexStateRows(st, false)
}

// 根行通道跳过所有历史派生行，未变化源不分配或遍历 gap、batch。
func (m *Manager) stateToIndexStateRows(st *persistedState, skipDerived bool) (stateindex.State, error) {
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
			aux.VerifiedRuns = saved.VerifiedRuns
			aux.IdentityConflicts = saved.IdentityConflicts
			aux.IdentityConflictClearances = saved.IdentityConflictClearances
			// 放弃凭据（首个账本条目的残余字段，与 ledgerCoreOf 同源）：nil/空时被
			// omitempty 省略，读回侧归一后两侧同形态可比（见 normalizePersistedState）。
			if len(saved.Ledger) > 0 {
				aux.Abandonments = saved.Ledger[0].Abandonments
			}
			aux.LedgerCore = core
			if len(saved.Ledger) > 1 {
				// 防御：首个条目之外的账本条目原样保留（现实中每源恰好一条）。
				aux.ExtraLedger = saved.Ledger[1:]
			}
			position, projection, gaps, batches, err := ledgerRowsOfOpt(key, saved, core, skipDerived)
			if err != nil {
				return stateindex.State{}, err
			}
			out.Positions = append(out.Positions, position)
			if projection != nil {
				out.Projections = append(out.Projections, *projection)
			}
			out.Gaps = append(out.Gaps, gaps...)
			out.Batches = append(out.Batches, batches...)
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
			RuleKey: binding.RuleKey, RelativePath: binding.RelativePath,
		})
	}
	return out, nil
}

// ledgerCoreOf 从账本条目提取残余字段。
func ledgerCoreOf(entry *ledger.Entry) *indexLedgerCore {
	return &indexLedgerCore{
		Key: entry.Key, Identity: entry.Identity, DeliveryPos: entry.Positions.Delivery,
		DeliveryState: entry.DeliveryState, Segments: entry.Segments, Rotations: entry.Rotations,
		ErrorCount: entry.ErrorCount, IngestSeq: entry.IngestSeq,
		// 指纹必须随其余残余字段一起落库：漏搬即等于「重启后指纹归零 ⇒ 段 3 判不一致 ⇒
		// 禁止按旧偏移续读」，把 w18 要消除的漂移重复摄取原样放回来（见 indexLedgerCore 注释）。
		PositionsFingerprint:   entry.PositionsFingerprint,
		PositionsFingerprintAt: entry.PositionsFingerprintAt,
		ConfirmedFingerprint:   entry.ConfirmedFingerprint,
		RequestDoneRanges:      entry.RequestDoneRanges,
		DeliveryEvidence:       entry.DeliveryEvidence,
	}
}

// stateToIndexStateScoped 是 stateToIndexState 的增量形态：账本派生行（gap/delivery_batch）
// 只按 changed 里的源构建，根行（source/position/projection/source_aux/instance_binding）恒为全量。
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
	full, err := m.stateToIndexStateRows(st, true)
	if err != nil {
		return stateindex.State{}, err
	}
	full.Gaps = partial.Gaps
	full.Batches = partial.Batches
	// source/position/projection/source_aux/instances 都在 full 里。
	return full, nil
}

// ledgerRowsOf 把「首个账本条目」拆成 position/gap/delivery_batch 行（spec §2.2 各表只存一处）。
func ledgerRowsOfOpt(key string, saved persistedSource, core *indexLedgerCore, skipDerived bool) (stateindex.PositionRow, *stateindex.ProjectionRow, []stateindex.GapRow, []stateindex.BatchRow, error) {
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
	if skipDerived {
		return position, projection, nil, nil, nil
	}
	gaps := make([]stateindex.GapRow, 0, len(entry.Gaps))
	for index, gap := range entry.Gaps {
		var spans []byte
		if len(gap.FoldSpans) > 0 {
			var err error
			spans, err = json.Marshal(gap.FoldSpans)
			if err != nil {
				return position, projection, nil, nil, fmt.Errorf("ingest: 编码缺口精确区间失败: %w", err)
			}
		}
		gaps = append(gaps, stateindex.GapRow{
			ID: int64(index), Key: key,
			StartPos: gap.StartPos, EndPos: gap.EndPos, Reason: gap.Reason, Detail: gap.Detail,
			Resolved: gap.Resolved, Resolution: gap.Resolution, FoldSpans: spans,
			FoldSpanMerges: gap.FoldSpanMerges, FoldSpansUnknown: gap.FoldSpansUnknown, ReasonCode: gap.ReasonCode,
			ResolvedAtUTC: gap.ResolvedAtUTC, ResolvedBy: gap.ResolvedBy,
			FirstSeenUnix: gap.FirstSeenUnix,
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
		var spans []ledger.PositionRange
		if len(row.FoldSpans) > 0 {
			if err := json.Unmarshal(row.FoldSpans, &spans); err != nil {
				return nil, fmt.Errorf("ingest: 解析 %s 缺口精确区间失败: %w", row.Key, err)
			}
		}
		gaps[row.Key] = append(gaps[row.Key], ledger.Gap{
			StartPos: row.StartPos, EndPos: row.EndPos, Reason: row.Reason, Detail: row.Detail,
			Resolved: row.Resolved, Resolution: row.Resolution, FoldSpans: spans,
			FoldSpanMerges: row.FoldSpanMerges, FoldSpansUnknown: row.FoldSpansUnknown || (len(spans) == 0 && strings.Contains(row.Detail, "超上限折叠")), ReasonCode: row.ReasonCode,
			ResolvedAtUTC: row.ResolvedAtUTC, ResolvedBy: row.ResolvedBy,
			FirstSeenUnix: row.FirstSeenUnix,
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
				Abandonments:    aux.Abandonments,
				ErrorCount:      core.ErrorCount,
				IngestSeq:       core.IngestSeq,
				// 指纹读回：与 ledgerCoreOf 成对，缺一侧就等于断链。旧索引无该字段时
				// core 上得到的是零值（""/0），这正是段 3 既有的「缺指纹」语义（判不一致、
				// 走对账补齐），所以这里**不报错、也不特判**——旧库照常加载，由下一轮采样补齐。
				PositionsFingerprint:   core.PositionsFingerprint,
				PositionsFingerprintAt: core.PositionsFingerprintAt,
				ConfirmedFingerprint:   core.ConfirmedFingerprint,
				RequestDoneRanges:      core.RequestDoneRanges,
				DeliveryEvidence:       core.DeliveryEvidence,
				AcquirePaused:          position.AcquirePaused,
				PauseReason:            position.PauseReason,
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
			EventsStoredThrough:  projection.EventsStoredThrough,
			ProjectionGeneration: projection.Generation,
			EventsStored:         aux.EventsStored,
			Events:               aux.Events,
			VerifiedRuns:         aux.VerifiedRuns,
			IdentityConflicts:    aux.IdentityConflicts,
			// 清空凭据必须随记录一起读回：漏掉它会让「重启后凭据消失」——
			// 那正好把本功能做成"无痕清空"，是本功能要防的反面。
			IdentityConflictClearances: aux.IdentityConflictClearances,
		}
	}

	for _, row := range rows.Instances {
		st.Instances[row.UUID] = InstanceBinding{
			UUID: row.UUID, TargetID: row.Namespace, Generation: row.Generation,
			Mode: pipeline.AcquireMode(row.Mode), WorkDir: row.WorkDir,
			RuleKey: row.RuleKey, RelativePath: row.RelativePath,
		}
	}
	normalizePersistedState(st)
	return st, nil
}

// normalizePersistedState 把状态归一化，使「对比两侧」与「重建结果」在同一形态下可比：
//   - nil 切片/映射 → 空（读取路径只依赖 len，二者行为等价；DB 表无法区分 nil 与空）。
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
			for i := range entry.Gaps {
				gap := &entry.Gaps[i]
				if len(gap.FoldSpans) == 0 && strings.Contains(gap.Detail, "超上限折叠") {
					gap.FoldSpansUnknown = true
				}
			}
			if entry.DeliveryBatches == nil {
				entry.DeliveryBatches = []ledger.DeliveryBatch{}
			}
			// 放弃凭据与 Gaps/Batches 同族归一：nil → 空切片，使「本来就没有」与
			// 「读了但为空」在比对口径下同形态（见 comparisonState）。
			if entry.Abandonments == nil {
				entry.Abandonments = []ledger.GapAbandonment{}
			}
			if entry.Segments == nil {
				entry.Segments = []ledger.Segment{}
			}
			if entry.Rotations == nil {
				entry.Rotations = []ledger.RotationLink{}
			}
		}
		if saved.WAL == nil {
			saved.WAL = []acquire.WALEntry{}
		}
		if saved.Events == nil {
			saved.Events = []logtypes.Event{}
		}
		sort.SliceStable(saved.WAL, func(i, j int) bool { return lessWALEntry(saved.WAL[i], saved.WAL[j]) })
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

// comparisonState 返回「比对口径」下的状态：在 normalizePersistedState 之上，把派生字段
// Positions.Delivery 按 ledger 的口径重算（ledger.Restore 本就重算它，见
// ledger.ContiguousDeliveryEnd），并按**同一判据**裁掉水位之下的历史投递批次。
//
// 为什么只在比较时重算：索引落库保留原值，便于断言字段确实被搬运；而比较必须忽略派生值噪声
// ——落库前的值是当时的推导，读回后按规格重算，二者语义相同，按原值比较会把一次正常重启
// 误判为不一致。
//
// 为什么还要在比较里裁剪：裁剪只发生在**写路径**上（ledger 的写方法），而两侧的基线可能
// 处于不同裁剪进度（例如一侧是裁剪前的快照）。裁剪是恒等元变换（判据与证明见
// ledger.PruneDeliveryBatches），归一后两侧仍逐字段可比，真正的搬运错误照样会被抓出来。
//
// 口径固定为「严格按水位」（keepRecent=0）：两侧同口径即可，开关只影响落库行数、不影响语义。
func comparisonState(st *persistedState) *persistedState {
	normalizePersistedState(st)
	for key, saved := range st.Sources {
		for index := range saved.Ledger {
			entry := &saved.Ledger[index]
			entry.RequestDoneRanges = ledger.RequestDoneRangesOf(entry)
			entry.DeliveryEvidence = ledger.DeliveryEvidenceOf(entry)
			entry.DeliveryBatches = prunedBatchesForComparison(entry)
			entry.Positions.Delivery = ledger.ContiguousDeliveryEnd(entry.DeliveryBatches, entry.Positions.Delivery)
		}
		st.Sources[key] = saved
	}
	return st
}

// prunedBatchesForComparison 把条目的投递批次按水位归一（失败即保留原列表，不新增失败面）。
//
// 它同时保证了「裁空」与「本来为空」在同一形态下可比：PruneDeliveryBatches 在裁空时返回
// 非 nil 的空切片（与 normalizePersistedState 的归一形态一致）。
//
// **水位必须是 Positions.Delivery**（2026-10-29 修正）：生产裁剪用
// `deliveryBatchWatermark(e) = Positions.Delivery`，而本函数此前传的是 `Positions.Reclaim`。
// 两者在 `Reclaim < Delivery` 时不同 ⇒ `verifyPruneIdentity` 面对的恒等元集合不一致
// ⇒ 迁移校验会把「可裁」与「不可裁」混在一张表里比，得出假不一致。
// 对比口径必须与生产口径**逐字相同**，否则这个函数就成了第二真源。
func prunedBatchesForComparison(entry *ledger.Entry) []ledger.DeliveryBatch {
	kept, dropped, err := ledger.PruneDeliveryBatches(entry.DeliveryBatches, entry.Positions.Delivery, 0)
	if err != nil {
		slog.Warn("迁移校验前的投递批次归一被守卫拦下，按完整历史比对（不影响采集正确性）",
			"logSourceID", entry.Key.LogSourceID, "generation", entry.Key.SourceGeneration,
			"delivery", entry.Positions.Delivery, "batches", len(entry.DeliveryBatches), "error", err)
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

// indexCommitBudgetOf 归一化「提交单元预算」：nil 表示用默认（每提交单元 128 行、目标 40ms；
// 真源是 stateindex.DefaultCommitMaxRows / DefaultCommitTarget，此处不再复写具体数字以免再次漂移）。
// 非法值（非正行数/耗时）在 stateindex.Normalized 里回退默认——配置误写不得让切分失效。
func indexCommitBudgetOf(configured *stateindex.CommitBudget) stateindex.CommitBudget {
	if configured == nil {
		return stateindex.DefaultCommitBudget()
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

// PersistNow 立即执行一次持久化并把本次周期的写入统计返回给调用方。
//
// 用途：真机/演练取「单次持久化」的现场读数——验收判据是「60 源下单次持久化 ≤50ms」，
// 需要能主动驱动一次并直接读到本次的行数/字节/耗时，而不是只能等轮询恰好触发。
// 语义与轮询路径完全一致（同一 persist），因此读到的就是生产口径。
//
// 口径：返回的是**整个周期**的合计（Chunks 是周期内切出的提交单元数，MaxChunkRows 是其中
// 最大单元的写+删行数）；验收线约束的**单提交单元**耗时请读 PersistLatency().P95/Max——
// 一次周期可能被切成多个单元（FR-498 切分），二者不可混用。
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
	// 采样环尾部连续同 CycleID 的样本即本次周期切出的全部提交单元，按单元汇总成周期合计。
	lastCycle := samples[len(samples)-1].CycleID
	stats := stateindex.Stats{
		Written: map[string]int{}, Deleted: map[string]int{}, Planned: map[string]int{},
	}
	var started, ended time.Time
	for _, sample := range samples {
		if sample.CycleID != lastCycle {
			continue
		}
		stats.RowsWritten += sample.RowsWritten
		stats.RowsDeleted += sample.RowsDeleted
		stats.BytesWritten += sample.BytesWritten
		stats.RowsPlanned += sample.RowsPlanned
		for name, count := range sample.Planned {
			stats.Planned[name] += count
		}
		stats.Chunks++
		if rows := sample.Rows(); rows > stats.MaxChunkRows {
			stats.MaxChunkRows = rows
		}
		if started.IsZero() || sample.StartedAt.Before(started) {
			started = sample.StartedAt
		}
		if finish := sample.StartedAt.Add(sample.Duration); finish.After(ended) {
			ended = finish
		}
	}
	stats.Duration = ended.Sub(started)
	return stats, nil
}

// ExportIndexJSON 只读导出索引内容为 persistedState 同等结构的 JSON（排障与回滚前快照）。
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
