package pipeline

// AcquireMode 标识采集通道。Paper 默认 FILE_PRIMARY。
type AcquireMode string

const (
	// ModeFilePrimary FileTailer 读 latest.log / 轮转分段（Paper 默认）。
	ModeFilePrimary AcquireMode = "FILE_PRIMARY"
	// ModeStdioPrimary 受管 STDIO Raw 分段（进程 stdout/stderr）。
	ModeStdioPrimary AcquireMode = "STDIO_PRIMARY"
)

// DefaultMode 返回契约默认采集模式。
func DefaultMode() AcquireMode { return ModeFilePrimary }

// Valid 报告模式是否已知。
func (m AcquireMode) Valid() bool {
	return m == ModeFilePrimary || m == ModeStdioPrimary
}
