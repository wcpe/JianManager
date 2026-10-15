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
	// ArchiveGlob 是归档文件相对工作目录的 glob（空 = 该核心不产出归档）。
	//
	// 注意 BungeeCord 的轮转产物是 `proxy.log.N`（数字后缀）而非 `.gz`，
	// 与 Bukkit 系的 `latest.log → *.gz` 形态不同。
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
	// 轮转产物为同目录的 proxy.log.N（非 gzip）。
	RuleKeyBungee: {
		Key:          RuleKeyBungee,
		LiveRelative: "proxy.log.0",
		ArchiveGlob:  "proxy.log.[0-9]*",
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
