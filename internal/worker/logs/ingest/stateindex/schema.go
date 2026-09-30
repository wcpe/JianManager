// Package stateindex 是 Worker 采集索引（FR-496）的嵌入式 SQLite 存储层。
//
// 背景：索引原先是一份由 ingest.Manager.persist **整本重写**的 JSON（`var/log/ingest.state.json`，
// 生产实测 307MB/13 源；历史事故中单源涨到 1.2GB 后每次持久化全量重写 → 持续 117MB/s、整机
// iowait 90%）。本包把索引落到本地嵌入式库（modernc.org/sqlite，纯 Go、无 cgo），写入
// **只针对本批次变更的行**（UPSERT），因此成本从 O(全量) 降到 O(变更行)。
//
// 库文件与连接模式（spec §2.1）：`var/log/ingest.index.db`；Worker 进程内**单写者单连接** +
// `journal_mode=WAL` + `synchronous=NORMAL`；读路径仅本进程（无跨进程共享）。
//
// 本包只承担「表结构 + 按行增量写入 + 按行读出」，不认识 ingest/ledger/acquire 的任何类型，
// 由上层（internal/worker/logs/ingest）把内存状态拆成行、并把行还原成内存状态，
// 从而保证「索引存储格式」与「上层语义」各自可独立测试。
package stateindex

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"
)

// tableCount 是索引表的数量（spec 五张规范表 + 三张工程扩展表）。
const tableCount = 8

// 表在 specs/计划数组中的下标。写入顺序即此顺序（父表在前，满足外键）。
const (
	tblSource = iota
	tblPosition
	tblGap
	tblProjection
	tblInstanceBinding
	tblSourceAux
	tblSourceWAL
	tblDeliveryBatch
)

// specDDL 是 spec §2.2 规定的五张表，**逐字保留**（仅补 IF NOT EXISTS 以便幂等建表），
// 使「按 spec 建的库」与「本实现建的库」在表形态上完全一致。
const specDDL = `
-- 每个日志源一行：身份与配置摘要
CREATE TABLE IF NOT EXISTS source (
  key            TEXT PRIMARY KEY,   -- LogSourceID/SourceGeneration
  log_source_id  TEXT NOT NULL,
  source_generation TEXT NOT NULL,
  storage_namespace TEXT NOT NULL,
  updated_at     INTEGER NOT NULL
);
-- 采集游标（可恢复到文件内偏移）
CREATE TABLE IF NOT EXISTS position (
  key        TEXT PRIMARY KEY REFERENCES source(key),
  read_pos   INTEGER NOT NULL,
  durable_pos INTEGER NOT NULL,
  reclaim_pos INTEGER NOT NULL,
  acquire_paused INTEGER NOT NULL DEFAULT 0,
  pause_reason   TEXT
);
-- 未解缺口（逐条可查、可人工解算）
-- 与 spec §2.2 的唯一差异：id 由 INTEGER PRIMARY KEY AUTOINCREMENT 改为「该源内缺口序号 +
-- 复合主键 (key, id)」。原因：全局 AUTOINCREMENT 的行身份无法在重启后稳定复现（同一缺口在
-- 不同进程里会拿到不同 id），而「只写变更行」要求行身份可稳定推导；复合主键仍满足 spec 的
-- 目的（逐条可查、可人工解算），且缺口顺序按 id 保持。其余列与 spec 逐字一致。
CREATE TABLE IF NOT EXISTS gap (
  id         INTEGER NOT NULL,   -- 该源内缺口序号（从 0 起，保持缺口顺序）
  key        TEXT NOT NULL REFERENCES source(key),
  start_pos  INTEGER NOT NULL,
  end_pos    INTEGER NOT NULL,
  reason     TEXT NOT NULL,
  detail     TEXT,
  resolved   INTEGER NOT NULL DEFAULT 0,
  resolution TEXT,
  PRIMARY KEY (key, id)
);
CREATE INDEX IF NOT EXISTS idx_gap_key_resolved ON gap(key, resolved);
-- 投影发布状态（重启增量对账的记账面）
CREATE TABLE IF NOT EXISTS projection (
  key        TEXT PRIMARY KEY REFERENCES source(key),
  generation TEXT NOT NULL,
  events_stored_through INTEGER NOT NULL DEFAULT 0,
  pending    INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
-- 实例绑定（stdout/stderr 采集所依赖）
CREATE TABLE IF NOT EXISTS instance_binding (
  uuid       TEXT PRIMARY KEY,
  namespace  TEXT NOT NULL,
  generation TEXT NOT NULL,
  mode       TEXT NOT NULL,
  work_dir   TEXT NOT NULL
);
`

// extensionDDL 是 spec §2.2「首版」未列、但**零行为变更所必需**的扩展表。
//
// 为什么必须有：spec §2.2 的五张表只覆盖 source/position/gap/projection/instance_binding，
// 而现有 `persistedSource` 与 `ledger.Entry` 还有若干字段必须落盘才能让上层行为逐一等价
// （账本身份与分段、投递批次、恢复引用、WAL 条目与引用、投影内联事件等）。把这些字段
// 塞进五张表的既有列会破坏其语义，故按「随数据量增长的部分独立成表、小体量元数据按源一行」
// 拆开：
//   - source_aux      每个源一行：SourceConfig JSON + persistedSource 其余字段的 JSON；
//   - source_wal      每条 WAL 一行（正文为 NULL 表示「引用条目」），是本索引唯一随
//     单源数据量增长的字段（1.2GB 事故的根因），必须按条 UPSERT/删除；
//   - delivery_batch  每个投递批次一行（批次随投递次数增长）。
//
// 所有扩展表都带 `fp` 列（source_aux/delivery_batch 的 fp 由本包计算并落库，source_wal 的
// fp 由调用方给出）：它是「这行是否变更」的判据，镜像与写入两侧都读同一处的 fp，避免两侧
// 各自计算导致的假阴性/假阳性。
const extensionDDL = `
CREATE TABLE IF NOT EXISTS source_aux (
  key     TEXT PRIMARY KEY REFERENCES source(key),
  config  TEXT,
  payload TEXT,
  fp      INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS source_wal (
  key          TEXT NOT NULL REFERENCES source(key),
  seq          INTEGER NOT NULL,
  event_id     TEXT NOT NULL,
  record_start INTEGER NOT NULL,
  record_end   INTEGER NOT NULL,
  appended     INTEGER NOT NULL DEFAULT 0,
  durable      INTEGER NOT NULL DEFAULT 0,
  body         TEXT,
  fp           INTEGER NOT NULL,
  PRIMARY KEY (key, seq, event_id, record_start, record_end)
);
CREATE INDEX IF NOT EXISTS idx_source_wal_key ON source_wal(key);
CREATE TABLE IF NOT EXISTS delivery_batch (
  key       TEXT NOT NULL REFERENCES source(key),
  ordinal   INTEGER NOT NULL,
  start_pos INTEGER NOT NULL,
  end_pos   INTEGER NOT NULL,
  state     TEXT NOT NULL,
  PRIMARY KEY (key, ordinal)
);
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// SourceRow 是 source 表的一行（身份与配置摘要）。
type SourceRow struct {
	Key                string
	LogSourceID        string
	SourceGeneration   string
	StorageNamespace   string
	UpdatedAtUnixMilli int64
}

// PositionRow 是 position 表的一行（采集游标）。
type PositionRow struct {
	Key           string
	ReadPos       uint64
	DurablePos    uint64
	ReclaimPos    uint64
	AcquirePaused bool
	PauseReason   string
}

// GapRow 是 gap 表的一行。ID 是该源内缺口的序号（从 0 起），用于保持缺口顺序。
type GapRow struct {
	ID         int64
	Key        string
	StartPos   uint64
	EndPos     uint64
	Reason     string
	Detail     string
	Resolved   bool
	Resolution string
}

// ProjectionRow 是 projection 表的一行（投影发布状态）。
type ProjectionRow struct {
	Key                 string
	Generation          string
	EventsStoredThrough uint64
	Pending             bool
	UpdatedAtUnixMilli  int64
}

// InstanceRow 是 instance_binding 表的一行。Namespace 对应 InstanceBinding.TargetID。
type InstanceRow struct {
	UUID       string
	Namespace  string
	Generation string
	Mode       string
	WorkDir    string
}

// AuxRow 是 source_aux 表的一行：spec 五表之外的附属数据。
// Config/Payload 为 nil 表示该键没有对应数据（分别对应「无配置」「无状态条目」）。
type AuxRow struct {
	Key     string
	Config  []byte
	Payload []byte
}

// WALRow 是 source_wal 表的一行。Body 为 nil 表示引用条目（正文已在事件段存储中）。
type WALRow struct {
	Key         string
	Seq         uint64
	EventID     string
	RecordStart uint64
	RecordEnd   uint64
	Appended    bool
	Durable     bool
	Body        []byte
	// FP 是**写入路径专用**的正文判据（不落库为独立列，落库的是合成后的行指纹）：
	// 调用方必须保证「同一 (Key, EventID, RecordStart, RecordEnd) 下 FP 相同 ⇒ Body 相同」。
	// 引用条目（Body 为 nil）填 0。
	FP uint64
}

// BatchRow 是 delivery_batch 表的一行。Ordinal 是该源内批次的序号（从 0 起）。
type BatchRow struct {
	Key      string
	Ordinal  int64
	StartPos uint64
	EndPos   uint64
	State    string
}

// State 是索引的全部内容（读出与写入共用同一形态：写入时表示「期望状态」）。
type State struct {
	Sources     []SourceRow
	Positions   []PositionRow
	Gaps        []GapRow
	Projections []ProjectionRow
	Instances   []InstanceRow
	Aux         []AuxRow
	WAL         []WALRow
	Batches     []BatchRow
}

// Counts 是各表行数，用于「库是否为空」判定与迁移校验的错误信息。
type Counts struct {
	Sources     int64
	Positions   int64
	Gaps        int64
	Projections int64
	Instances   int64
	Aux         int64
	WAL         int64
	Batches     int64
}

// Empty 报告索引是否不含任何数据行（不含 meta）。
func (c Counts) Empty() bool {
	return c == Counts{}
}

// loadColumns 是加载路径的列清单（不含 fp 列；顺序即 appendRow 的取值下标）。
var loadColumns = [tableCount][]string{
	tblSource:          {"key", "log_source_id", "source_generation", "storage_namespace", "updated_at"},
	tblPosition:        {"key", "read_pos", "durable_pos", "reclaim_pos", "acquire_paused", "pause_reason"},
	tblGap:             {"key", "id", "start_pos", "end_pos", "reason", "detail", "resolved", "resolution"},
	tblProjection:      {"key", "generation", "events_stored_through", "pending", "updated_at"},
	tblInstanceBinding: {"uuid", "namespace", "generation", "mode", "work_dir"},
	tblSourceAux:       {"key", "config", "payload"},
	tblSourceWAL:       {"key", "seq", "event_id", "record_start", "record_end", "appended", "durable", "body"},
	tblDeliveryBatch:   {"key", "ordinal", "start_pos", "end_pos", "state"},
}

// rowData 是一行「本次期望写入」的数据。
//
// values 惰性求值：未变更的行不会调用它，因此不会为未变更的 WAL 正文重复做 JSON 编码——
// 这是「只写变更行」在 CPU 侧也不会退化到 O(全量) 的关键。
type rowData struct {
	key    string
	fp     uint64
	values func() ([]any, error)
}

// tableSpec 描述一张表如何镜像、如何差异、如何写入。
type tableSpec struct {
	name string
	// keyCols 是主键列名（镜像以它们的规范编码为键）。
	keyCols []string
	// mirrorSQL 读取 (主键列..., fp)；fpSelf 为 true 时 fp 取自表内 fp 列。
	mirrorSQL string
	fpSelf    bool
	// upsert/deleteStmt 是写入与删除语句。
	upsert     string
	deleteStmt string
	// orderBy 是加载时的排序（保持行序语义）。
	orderBy string
}

// specs 按 tblXxx 下标排列；顺序即写入顺序（父表在前，满足外键约束）。
var specs = [tableCount]tableSpec{
	tblSource: {
		name:      "source",
		keyCols:   []string{"key"},
		mirrorSQL: "SELECT key, log_source_id, source_generation, storage_namespace FROM source",
		upsert: `INSERT INTO source (key, log_source_id, source_generation, storage_namespace, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET log_source_id=excluded.log_source_id,
				source_generation=excluded.source_generation, storage_namespace=excluded.storage_namespace,
				updated_at=excluded.updated_at`,
		deleteStmt: "DELETE FROM source WHERE key = ?",
		orderBy:    "key",
	},
	tblPosition: {
		name:      "position",
		keyCols:   []string{"key"},
		mirrorSQL: "SELECT key, read_pos, durable_pos, reclaim_pos, acquire_paused, pause_reason FROM position",
		upsert: `INSERT INTO position (key, read_pos, durable_pos, reclaim_pos, acquire_paused, pause_reason)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET read_pos=excluded.read_pos, durable_pos=excluded.durable_pos,
				reclaim_pos=excluded.reclaim_pos, acquire_paused=excluded.acquire_paused,
				pause_reason=excluded.pause_reason`,
		deleteStmt: "DELETE FROM position WHERE key = ?",
		orderBy:    "key",
	},
	tblGap: {
		name:      "gap",
		keyCols:   []string{"key", "id"},
		mirrorSQL: "SELECT key, id, start_pos, end_pos, reason, detail, resolved, resolution FROM gap",
		upsert: `INSERT INTO gap (key, id, start_pos, end_pos, reason, detail, resolved, resolution)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(key, id) DO UPDATE SET start_pos=excluded.start_pos, end_pos=excluded.end_pos,
				reason=excluded.reason, detail=excluded.detail, resolved=excluded.resolved,
				resolution=excluded.resolution`,
		deleteStmt: "DELETE FROM gap WHERE key = ? AND id = ?",
		orderBy:    "key, id",
	},
	tblProjection: {
		name:      "projection",
		keyCols:   []string{"key"},
		mirrorSQL: "SELECT key, generation, events_stored_through, pending FROM projection",
		upsert: `INSERT INTO projection (key, generation, events_stored_through, pending, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET generation=excluded.generation,
				events_stored_through=excluded.events_stored_through, pending=excluded.pending,
				updated_at=excluded.updated_at`,
		deleteStmt: "DELETE FROM projection WHERE key = ?",
		orderBy:    "key",
	},
	tblInstanceBinding: {
		name:      "instance_binding",
		keyCols:   []string{"uuid"},
		mirrorSQL: "SELECT uuid, namespace, generation, mode, work_dir FROM instance_binding",
		upsert: `INSERT INTO instance_binding (uuid, namespace, generation, mode, work_dir)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(uuid) DO UPDATE SET namespace=excluded.namespace, generation=excluded.generation,
				mode=excluded.mode, work_dir=excluded.work_dir`,
		deleteStmt: "DELETE FROM instance_binding WHERE uuid = ?",
		orderBy:    "uuid",
	},
	tblSourceAux: {
		name:      "source_aux",
		keyCols:   []string{"key"},
		mirrorSQL: "SELECT key, fp FROM source_aux",
		fpSelf:    true,
		upsert: `INSERT INTO source_aux (key, config, payload, fp) VALUES (?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET config=excluded.config, payload=excluded.payload, fp=excluded.fp`,
		deleteStmt: "DELETE FROM source_aux WHERE key = ?",
		orderBy:    "key",
	},
	tblSourceWAL: {
		name:      "source_wal",
		keyCols:   []string{"key", "seq", "event_id", "record_start", "record_end"},
		mirrorSQL: "SELECT key, seq, event_id, record_start, record_end, fp FROM source_wal",
		fpSelf:    true,
		upsert: `INSERT INTO source_wal (key, seq, event_id, record_start, record_end, appended, durable, body, fp)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(key, seq, event_id, record_start, record_end) DO UPDATE SET
				appended=excluded.appended, durable=excluded.durable, body=excluded.body, fp=excluded.fp`,
		deleteStmt: "DELETE FROM source_wal WHERE key = ? AND seq = ? AND event_id = ? AND record_start = ? AND record_end = ?",
		orderBy:    "seq, event_id, record_start, record_end",
	},
	tblDeliveryBatch: {
		name:      "delivery_batch",
		keyCols:   []string{"key", "ordinal"},
		mirrorSQL: "SELECT key, ordinal, start_pos, end_pos, state FROM delivery_batch",
		upsert: `INSERT INTO delivery_batch (key, ordinal, start_pos, end_pos, state) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(key, ordinal) DO UPDATE SET start_pos=excluded.start_pos, end_pos=excluded.end_pos,
				state=excluded.state`,
		deleteStmt: "DELETE FROM delivery_batch WHERE key = ? AND ordinal = ?",
		orderBy:    "key, ordinal",
	},
}

// fingerprint 计算一组列值的变更判据。
//
// 类型先归一化（bool→0/1、有符号整数→int64、无符号整数→int64），使「写入侧传入的 Go 值」与
// 「SQLite 读回的值」在同一逻辑内容下得到同一结果——镜像与写入两侧因此可以共用同一函数。
func fingerprint(values ...any) uint64 {
	h := fnv.New64a()
	for _, value := range values {
		encodeValue(h, value)
	}
	return h.Sum64()
}

// encodeValue 把单个值按「带类型标记 + 长度前缀」写入哈希，避免不同列拼接歧义
// （例如 ("ab","c") 与 ("a","bc") 必须得到不同结果）。
func encodeValue(h interface{ Write([]byte) (int, error) }, value any) {
	switch v := value.(type) {
	case nil:
		_, _ = h.Write([]byte{0})
	case bool:
		if v {
			_, _ = h.Write([]byte{1, 1})
		} else {
			_, _ = h.Write([]byte{1, 0})
		}
	case int64:
		_, _ = h.Write([]byte{2})
		encodeInt(h, v)
	case int:
		_, _ = h.Write([]byte{2})
		encodeInt(h, int64(v))
	case uint64:
		_, _ = h.Write([]byte{2})
		encodeInt(h, int64(v))
	case string:
		_, _ = h.Write([]byte{3})
		encodeBytes(h, []byte(v))
	case []byte:
		_, _ = h.Write([]byte{4})
		encodeBytes(h, v)
	default:
		// 其余类型（不应出现）按字符串处理，保证不会静默当成相同内容。
		_, _ = h.Write([]byte{9})
		encodeBytes(h, []byte(fmt.Sprintf("%v", v)))
	}
}

func encodeInt(h interface{ Write([]byte) (int, error) }, v int64) {
	var buf [8]byte
	uv := uint64(v)
	for i := 0; i < 8; i++ {
		buf[i] = byte(uv >> (8 * i))
	}
	_, _ = h.Write(buf[:])
}

func encodeBytes(h interface{ Write([]byte) (int, error) }, b []byte) {
	var buf [8]byte
	n := uint64(len(b))
	for i := 0; i < 8; i++ {
		buf[i] = byte(n >> (8 * i))
	}
	_, _ = h.Write(buf[:])
	_, _ = h.Write(b)
}

// mirrorKey 把主键列值编码为镜像表的键。
//
// 文本值用「类型标记 + 长度前缀 + 内容」编码，因此含 "|" 等分隔符的键（如日志源路径）也不会
// 与相邻列混淆；解码见 decodeMirrorKey。
func mirrorKey(values ...any) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		switch v := value.(type) {
		case nil:
			parts = append(parts, "-")
		case bool:
			parts = append(parts, "b"+strconv.FormatBool(v))
		case int64:
			parts = append(parts, "i"+strconv.FormatInt(v, 10))
		case int:
			parts = append(parts, "i"+strconv.FormatInt(int64(v), 10))
		case uint64:
			// 与 int64 统一编码：加载侧从库中读回的就是 int64，两边的镜像键必须一致。
			// 位置/序号实际取值远小于 2^63（见 toInt64），不会出现负数。
			parts = append(parts, "i"+strconv.FormatInt(int64(v), 10))
		case string:
			parts = append(parts, "s"+strconv.Itoa(len(v))+":"+v)
		default:
			parts = append(parts, "s"+strconv.Itoa(len(fmt.Sprintf("%v", v)))+":"+fmt.Sprintf("%v", v))
		}
	}
	return strings.Join(parts, "|")
}

// decodeMirrorKey 把镜像键还原为 deleteStmt 需要的主键实参。
func decodeMirrorKey(spec tableSpec, key string) ([]any, error) {
	values := make([]any, 0, len(spec.keyCols))
	rest := key
	for index := 0; index < len(spec.keyCols); index++ {
		if rest == "" {
			return nil, fmt.Errorf("stateindex: %s 主键编码不合法: %q", spec.name, key)
		}
		part, tail, err := consumeKeyPart(rest)
		if err != nil {
			return nil, fmt.Errorf("stateindex: %s 主键编码不合法: %q", spec.name, key)
		}
		values = append(values, part...)
		rest = tail
	}
	if rest != "" {
		return nil, fmt.Errorf("stateindex: %s 主键编码不合法: %q", spec.name, key)
	}
	return values, nil
}

// consumeKeyPart 消费镜像键的一段，返回该段的实参（0 或 1 个）与剩余内容。
//
// 文本段自带长度前缀：按长度消费内容，因此文本里的 "|" 不会与段分隔符混淆。
func consumeKeyPart(text string) ([]any, string, error) {
	kind := text[0]
	payload := text[1:]
	switch kind {
	case '-':
		return []any{nil}, afterSeparator(payload), nil
	case 'b':
		head, tail := splitHead(payload)
		switch head {
		case "true":
			return []any{true}, tail, nil
		case "false":
			return []any{false}, tail, nil
		default:
			return nil, "", fmt.Errorf("非法的布尔段 %q", head)
		}
	case 'i':
		head, tail := splitHead(payload)
		value, err := strconv.ParseInt(head, 10, 64)
		if err != nil {
			return nil, "", err
		}
		return []any{value}, tail, nil
	case 'u':
		head, tail := splitHead(payload)
		value, err := strconv.ParseUint(head, 10, 64)
		if err != nil {
			return nil, "", err
		}
		return []any{int64(value)}, tail, nil
	case 's':
		colon := strings.IndexByte(payload, ':')
		if colon < 0 {
			return nil, "", fmt.Errorf("文本段缺少长度前缀")
		}
		length, err := strconv.Atoi(payload[:colon])
		if err != nil {
			return nil, "", err
		}
		contentStart := colon + 1
		if length > len(payload)-contentStart {
			return nil, "", fmt.Errorf("文本段长度不足（声明 %d，剩余 %d）", length, len(payload)-contentStart)
		}
		return []any{payload[contentStart : contentStart+length]}, afterSeparator(payload[contentStart+length:]), nil
	default:
		return nil, "", fmt.Errorf("未知的类型标记 %q", string(kind))
	}
}

// splitHead 切出「直到下一个 |」的段内容。
func splitHead(payload string) (string, string) {
	if index := strings.IndexByte(payload, '|'); index >= 0 {
		return payload[:index], payload[index+1:]
	}
	return payload, ""
}

// afterSeparator 吃掉段与段之间的分隔符（无后续段时返回空串）。
func afterSeparator(rest string) string {
	if strings.HasPrefix(rest, "|") {
		return rest[1:]
	}
	return rest
}

// 位置是文件内字节偏移、序号是条目/缺口计数，实际取值远小于 2^63；超过时按最大值截断并
// 由调用方在加载时原样读回为无符号数（不会出现负数）。
// toInt64 把无符号位置/序号收敛到 int64 落库。
//
// 位置是文件内字节偏移、序号是条目/缺口计数，实际取值远小于 2^63；超过时按最大值截断并
// 由加载路径原样读回为无符号数（不会出现负数）。
func toInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// toUint64 把库中读回的整数还原为无符号位置/序号。
func toUint64(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

// sortState 对 State 的各切片按稳定顺序归一化（不改变任何语义，只固定行序）。
//
// 为什么需要：source_wal 以事件身份为主键（同一 seq 在裁剪后可能被复用，故 seq 不唯一），
// 加载顺序按 Seq 排序；而恢复路径（acquire.RestoreMixed）本就按 Seq 重排，行序不承载语义。
// 迁移校验逐字段比对前统一排序，可避免「仅顺序不同」被误判为不一致。
func sortState(st *State) {
	sort.SliceStable(st.Sources, func(i, j int) bool { return st.Sources[i].Key < st.Sources[j].Key })
	sort.SliceStable(st.Positions, func(i, j int) bool { return st.Positions[i].Key < st.Positions[j].Key })
	sort.SliceStable(st.Gaps, func(i, j int) bool {
		if st.Gaps[i].Key != st.Gaps[j].Key {
			return st.Gaps[i].Key < st.Gaps[j].Key
		}
		return st.Gaps[i].ID < st.Gaps[j].ID
	})
	sort.SliceStable(st.Projections, func(i, j int) bool { return st.Projections[i].Key < st.Projections[j].Key })
	sort.SliceStable(st.Instances, func(i, j int) bool { return st.Instances[i].UUID < st.Instances[j].UUID })
	sort.SliceStable(st.Aux, func(i, j int) bool { return st.Aux[i].Key < st.Aux[j].Key })
	sort.SliceStable(st.WAL, func(i, j int) bool { return lessWAL(st.WAL[i], st.WAL[j]) })
	sort.SliceStable(st.Batches, func(i, j int) bool {
		if st.Batches[i].Key != st.Batches[j].Key {
			return st.Batches[i].Key < st.Batches[j].Key
		}
		return st.Batches[i].Ordinal < st.Batches[j].Ordinal
	})
}

// lessWAL 是 WAL 行的规范顺序（与 orderBy 一致）。
func lessWAL(a, b WALRow) bool {
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
