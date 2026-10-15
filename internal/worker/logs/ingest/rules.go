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
	// 「可导入」是这里的判据：导入器目前只有 gzip 一种实现（acquire.ImportGzipBounded），
	// 故轮转产物非 gzip 的核心（BungeeCord 的 `proxy.log.N` 是纯文本，与 Bukkit 系的
	// `latest.log → *.gz` 形态不同）必须如实置空，否则每次轮转都会留下一条不可消解的
	// ARCHIVE_GZIP_CORRUPT 缺口（详见 bungee 规则处的说明）。
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
	// 轮转产物为同目录的 proxy.log.N（**纯文本**）。
	//
	// 归档 glob 为空（2026-10-15 复核）。Rule.ArchiveGlob 的口径是「空 = 该核心不产出归档」，
	// BungeeCord 正属此列，理由有两层：
	//  ① 形态不兼容：归档导入器只支持 gzip（acquire.ImportGzipBounded），而 discoverSourceArchives
	//     不做任何形态过滤 ⇒ 每个 proxy.log.N 都被登记为 kind=gzip 段并记一条
	//     `ARCHIVE_GZIP_CORRUPT` 缺口，一条也导不进来；
	//  ② 后果是**阻断级**：该原因不在自动消解白名单（ledger/gap_bounds.go），而 ResumeAcquire
	//     对任何未消解缺口一律拒绝 ⇒ bungee 源一旦被暂停（积压/容量，生产常态）就**永久停采**，
	//     只能人工裁定缺口。
	// 历史两版 glob（`proxy.log.[0-9]*` 与收窄后的 `proxy.log.[1-9]*`）分别还多踩了
	// 「把当前活跃日志/0 字节锁文件当归档」与「形态本身就导不进来」两个坑；
	// 按同因相邻合并上报的缺口也无法证明只有锁文件在失败。
	//
	// **已知能力缺口（登记在此，勿当已解决）**：BungeeCord 的**纯文本**轮转历史不在本项
	// 采集范围内。要把它纳入，需要另立 FR（新增纯文本归档导入器或按 gzip 魔数分流），
	// 不属本项范围。
	RuleKeyBungee: {
		Key:          RuleKeyBungee,
		LiveRelative: "proxy.log.0",
		ArchiveGlob:  "",
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
