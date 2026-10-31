package pipeline

// AcquireMode 标识采集通道。Paper 默认 FILE_PRIMARY。
type AcquireMode string

const (
	// ModeFilePrimary FileTailer 读 latest.log / 轮转分段（Paper 默认）。
	ModeFilePrimary AcquireMode = "FILE_PRIMARY"
	// ModeStdioPrimary 受管 STDIO Raw 分段（进程 stdout/stderr）。
	ModeStdioPrimary AcquireMode = "STDIO_PRIMARY"
	// ModePushOnly 纯推送接收源：不由 Worker 主动读取任何物理载体（无 tailer、无 reader），
	// 事件只经 Pipeline.Ingest 从外部（如 HTTP 接收端点）注入。它仍拥有自己的 ledger + WAL，
	// 因此推送进来的事件走与文件源完全相同的攒批 / gzip / 流标签 / 背压 / 丢弃语义，
	// 满足「复用现有 pipeline、不开独立队列」的硬约束（FR-511）。
	//
	// 为什么必须专门一个模式（而非复用 FILE_PRIMARY 并置空 Path）：WAL 的真实积压上限
	// 由 Append 路径执行，而文件源的 Read/Delivery 水位是由 FileTailer 推进的字节游标；
	// 若把 HTTP 事件塞进文件源的 WAL，会把推送事件的 record 区间混进该文件源的字节游标，
	// 污染其回收 / 对账口径。PushOnly 源没有物理载体，水位完全由 Ingest 驱动，互不干扰。
	ModePushOnly AcquireMode = "PUSH_ONLY"
)

// DefaultMode 返回契约默认采集模式。
func DefaultMode() AcquireMode { return ModeFilePrimary }

// Valid 报告模式是否已知。
func (m AcquireMode) Valid() bool {
	return m == ModeFilePrimary || m == ModeStdioPrimary || m == ModePushOnly
}
