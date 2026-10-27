package eventstore

import "sync/atomic"

// 本文件提供**耐久调用（fsync 类）计数器**：只为取证与回归服务，生产语义零影响。
//
// 为什么需要它（机械盘 IO 收敛的验收依据）：机械盘上每次 fsync 基本等于一次寻道，
// 「一次 Append 触发了几次 fsync」是唯一可量化、且不依赖具体介质速度的指标；墙钟耗时
// 会被页缓存、RAID 缓存与队列深度污染，不能用来证明「IO 次数真的少了」。
//
// 计数点必须落在**真正发起 syscall 的那一行旁边**，否则统计值会与实际 IO 脱节。
const (
	// obsSyncSegment 是段数据文件的 fsync（崩溃一致性**必需**：调用方随后才会在其
	// 元数据里引用这些事件）。
	obsSyncSegment = iota
	// obsSyncManifestFile 是 MANIFEST.json 临时文件的 fsync。
	obsSyncManifestFile
	// obsSyncDirectory 是清单发布后的目录 fsync（保证 rename 与新建段目录项耐久）。
	obsSyncDirectory
)

// syncCounters 按类别统计耐久 syscall 次数。
var syncCounters [3]atomic.Int64

// observeSync 在发起耐久 syscall 之前调用（顺序无关：只要与 syscall 同处一条路径）。
func observeSync(kind int) {
	syncCounters[kind].Add(1)
}

// SyncCounts 返回各类耐久调用累计次数（段数据 / 清单文件 / 目录）。
//
// 仅供测试与运维观测使用；跨测试用例会累加，取差值即可得到单用例成本。
func SyncCounts() (segment, manifest, directory int64) {
	return syncCounters[obsSyncSegment].Load(),
		syncCounters[obsSyncManifestFile].Load(),
		syncCounters[obsSyncDirectory].Load()
}
