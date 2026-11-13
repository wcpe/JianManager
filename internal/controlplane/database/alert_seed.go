package database

import (
	"log/slog"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// defaultAlertRuleSeed 是一条要预置的告警规则。
type defaultAlertRuleSeed struct {
	UUID        string
	Name        string
	Level       string
	Metric      string
	Operator    string
	Threshold   float64
	DurationSec int
}

// defaultAlertRuleSeeds 是 CP 首次启动时预置的推荐规则。
//
// # 为什么预置（而不是让用户从零建）
//
// 生产实测：`alert_rules` 表里一条规则都没有。指标采了、时序库写了，
// 但没有任何东西在看它们——高水位、采集故障、VL 磁盘满全部无人知晓。
// 根因不是“用户不想配”，是“不知道有哪些可用指标、阈值该写多少”。
// 预置几条推荐值，至少让运维在 UI 里能看到“这东西该怎么配”。
//
// # 为什么不直接启用
//
// 阈值是业务判断：一个跑 19 个游戏服的节点和一个测试节点，“磁盘 90% 告警”的
// 意义完全不同。替用户默认打开告警，等于在他们的告警渠道里凭空塞消息。
// 所以全部 `enabled=false`——规则可见、默认不生效，改一下开关就能用。
//
// # 为什么没有 channel_ids
//
// 告警渠道（QQ/邮件等）也得用户先配好。预置规则不绑定渠道，
// 用户启用时自己选——否则会把通知发到不相关的地方。
var defaultAlertRuleSeeds = []defaultAlertRuleSeed{
	{
		UUID:        "seed-node-mem-high",
		Name:        "内存占用过高",
		Level:       model.AlertLevelWarn,
		Metric:      model.MetricNodeMemPct,
		Operator:    ">=",
		Threshold:   90,
		DurationSec: 300,
	},
	{
		UUID:        "seed-node-disk-high",
		Name:        "磁盘占用过高",
		Level:       model.AlertLevelCritical,
		Metric:      model.MetricNodeDiskPct,
		Operator:    ">=",
		Threshold:   90,
		DurationSec: 300,
	},
	{
		UUID:        "seed-node-iowait-high",
		Name:        "磁盘 IO 等待过高",
		Level:       model.AlertLevelWarn,
		Metric:      model.MetricNodeIOWait,
		Operator:    ">=",
		Threshold:   50,
		DurationSec: 300,
	},
	{
		UUID:        "seed-log-ingest-down",
		Name:        "日志采集不可用",
		Level:       model.AlertLevelCritical,
		Metric:      model.MetricLogIngestHealthy,
		Operator:    "==",
		Threshold:   0,
		DurationSec: 120,
	},
	{
		UUID:        "seed-log-vl-disk-high",
		Name:        "日志存储磁盘接近上限",
		Level:       model.AlertLevelWarn,
		Metric:      model.MetricLogVLDiskPct,
		Operator:    ">=",
		Threshold:   90,
		DurationSec: 300,
	},
}

// seedDefaultAlertRules 幂等地预置推荐告警规则。
//
// 幂等依据：按 UUID 判断是否已存在。已有同 UUID 的规则（无论用户后来怎么改阈值/启用）
// **一律不覆盖**——那可能是用户已经调过的值，覆盖等于把他们的配置偷换回默认。
func seedDefaultAlertRules(db *gorm.DB) error {
	if !db.Migrator().HasTable("alert_rules") {
		return nil
	}

	created := 0
	for _, seed := range defaultAlertRuleSeeds {
		var exists int64
		if err := db.Model(&model.AlertRule{}).Where("uuid = ?", seed.UUID).Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		rule := model.AlertRule{
			UUID:        seed.UUID,
			Name:        seed.Name,
			TriggerType: model.AlertTriggerMetric,
			Level:       seed.Level,
			TargetType:  "node",
			Metric:      seed.Metric,
			Operator:    seed.Operator,
			Threshold:   seed.Threshold,
			DurationSec: seed.DurationSec,
			// 默认**不启用**：阈值是业务判断，不在用户告警渠道里凭空塞消息。
			Enabled: false,
		}
		if err := db.Create(&rule).Error; err != nil {
			// 单条失败不阻断其它条：预置是体验改进，不是启动关键路径。
			slog.Warn("预置告警规则失败（不阻断启动）", "uuid", seed.UUID, "error", err)
			continue
		}
		// ⚠ Enabled 带 `gorm:"default:true"`：Create 对 bool 零值（false）**不写入**，
		// 而是交给数据库默认值 ⇒ 实际落库变成 true。这是 GORM 的零值 vs 默认值陷阱。
		// 必须显式再改一次，否则「默认不启用」这个核心约束就失效了。
		//
		// 不用 Select("*") 绕过：那会把 DeletedAt 等零值也强制写进去，
		// 语义上比「创建后再改一个字段」更难解释。
		if err := db.Model(&model.AlertRule{}).Where("uuid = ?", seed.UUID).
			Update("enabled", false).Error; err != nil {
			slog.Warn("预置告警规则未能置为未启用（不阻断启动）", "uuid", seed.UUID, "error", err)
		}
		created++
	}
	if created > 0 {
		slog.Info("已预置推荐告警规则（默认未启用，可在告警页开启）", "created", created)
	}
	return nil
}
