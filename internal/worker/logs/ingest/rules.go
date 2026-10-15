// 日志采集规则表（FR-502）。
//
// 背景：实例活动日志路径原先在 instances.go 被三段硬编码为
// `WorkDir/logs/latest.log`，无任何按核心类型的分支，导致 BungeeCord 的
// `proxy.log.0`（工作目录根、非 logs/ 子目录）永远不被发现。
//
// 本规则表以「核心类型 → 日志布局」的映射取代硬编码，由 CP 按实例
// `role` + `launch_spec.coreJar` 推导后经 proto 下发规则名。
package ingest

// Rule 描述一种服务端核心的日志布局。
type Rule struct {
	// Key 是规则名，与 proto `log_rule_key` 取值一致。
	Key string
	// LiveRelative 是活动日志相对实例工作目录的路径。
	LiveRelative string
	// ArchiveGlob 是归档文件相对工作目录的 glob（空 = 该核心不产出**可导入**的归档）。
	//
	// 「可导入」是这里的判据，而它取决于**形态**：归档导入器按文件头（gzip 魔数 `1f 8b`）
	// 把压缩归档分到 gzip 实现、把其余分到纯文本实现（acquire.DetectArchiveFormat），
	// 两种形态都被支持。故只有当某核心的轮转产物是**两种形态都不属于**的东西时，
	// 才应把本字段置空——那是「能力缺口」，而不是「路径写法」。
	ArchiveGlob string
}

// 规则 key 常量。
const (
	RuleKeyBukkit    = "bukkit"
	RuleKeyBungee    = "bungee"
	RuleKeyWaterfall = "waterfall"
	RuleKeyVelocity  = "velocity"
)

// builtinRules 是四条内置规则（FR-502 §3.3）。
//
// 路径一律使用正斜杠书写，由调用方按目标平台拼接（Windows 下 filepath.Join
// 会自行归一化为反斜杠）。
var builtinRules = map[string]Rule{
	// Paper / Spigot / Bukkit 系：活动日志在 logs/，归档为同目录 *.gz。
	// 这是历史行为，必须逐字保持不变。
	RuleKeyBukkit: {
		Key:          RuleKeyBukkit,
		LiveRelative: "logs/latest.log",
		ArchiveGlob:  "logs/*.gz",
	},
	// BungeeCord：活动日志在**工作目录根**的 proxy.log.0，
	// 轮转产物为同目录的 proxy.log.N（**纯文本**，无扩展名）。
	//
	// glob 收窄为 `proxy.log.[1-9]*`，两道排除：
	//  ① 首字符限定 1-9 ⇒ 排除 `proxy.log.0`（**当前活跃日志**：它由 FileTailer 采集，
	//     再按归档导一次就是同一份字节的双计）；
	//  ② 同一写法也排除了 `proxy.log.0.lck`（java.util.logging 为**当前**日志文件持有的
	//     0 字节锁文件）。
	// 注意 `proxy.log.N.lck`（N≥1，轮转文件的锁）仍会被匹配：它不是归档，但**不再是缺口**
	// ——纯文本路径把它处置为「空归档」（0 事件、无缺口、直接记为已导入），
	// 见 acquire.TestImportTextEmptyRotatedFileLeavesNoGap。
	//
	// 为什么可以恢复采集（本项修复）：轮转产物是**纯文本**且没有扩展名，形态由导入器读
	// **文件头**判定后分流到纯文本实现（acquire.ImportTextBounded），并登记为形态专属的
	// kind（ledger.SegmentRotatedText，不复用 gzip——复用会让统计与诊断失真）。
	//
	// 与「glob 置空」那段历史的区别（勿把结论记反）：置空的原因是**当时没有纯文本实现**——
	// 任何被匹配到的文件都会以 ARCHIVE_GZIP_CORRUPT 失败，而该原因不在自动消解白名单
	// （ledger/gap_bounds.go），ResumeAcquire 又对任何未消解缺口一律拒绝 ⇒ bungee 源一旦被
	// 暂停（积压/容量，生产常态）就**永久停采**。那时 glob 的两种写法（`proxy.log.[0-9]*`
	// 与 `proxy.log.[1-9]*`）里，前者还多踩了「把当前活跃日志与它的锁文件当归档」这个坑；
	// 现在形态有实现、退化形态有明确处置，故按②收窄后恢复。
	RuleKeyBungee: {
		Key:          RuleKeyBungee,
		LiveRelative: "proxy.log.0",
		ArchiveGlob:  "proxy.log.[1-9]*",
	},
	// Waterfall：BungeeCord 的分支，日志布局按 Bukkit 约定输出到 logs/。
	RuleKeyWaterfall: {
		Key:          RuleKeyWaterfall,
		LiveRelative: "logs/latest.log",
		ArchiveGlob:  "logs/*.gz",
	},
	// Velocity：独立日志文件名 velocity.log，归档为同目录 *.gz。
	RuleKeyVelocity: {
		Key:          RuleKeyVelocity,
		LiveRelative: "logs/velocity.log",
		ArchiveGlob:  "logs/*.gz",
	},
}

// Lookup 按规则名取规则；未知或空 key 一律回退 bukkit。
//
// 回退语义是 FR-502 的向后兼容判据：旧 CP 不下发 log_rule_key（空串），
// 此时必须与历史行为逐字一致（logs/latest.log + logs/*.gz）。
func Lookup(key string) Rule {
	if r, ok := builtinRules[key]; ok {
		return r
	}
	return builtinRules[RuleKeyBukkit]
}
