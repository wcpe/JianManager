package model

import "time"

// BusinessEvent 是 JBIS 通用业务事件信封表（插件无关汇聚底座，FR-116 底座 / FR-122，见 ADR-028）。
//
// 探针经反向 WS 桥上报的业务域事件（economy/inventory…）一律按本表落库：CP **插件无关**，只认
// domain/action/payload 信封 + dedupKey，不理解具体业务语义。按 (Domain, DedupKey) 唯一去重应对桥的
// **至少一次投递**与跨节点重试（同键重发 insert-or-ignore，不重复计数）。新增业务域**无需改表**。
//
// 数据所有权（架构不变量）：JM 只存汇聚镜像 + 操作审计，业务真源仍在各插件存储；本表是"汇聚镜像"的通用底座，
// 高价值域（经济）另有结构化专表（[EconomyBalanceMirror] / [EconomyLedgerEntry]）供高效查询与对账。
type BusinessEvent struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Domain 业务域命名空间（economy/inventory…）；与 DedupKey 组成唯一去重键。
	Domain string `gorm:"type:varchar(32);not null;uniqueIndex:idx_be_domain_dedup,priority:1;index:idx_be_domain_time,priority:1" json:"domain"`
	// DedupKey 业务事件去重锚点（经济域为 mce ledgerId 字符串，全局唯一稳定）。
	DedupKey string `gorm:"column:dedup_key;type:varchar(128);not null;uniqueIndex:idx_be_domain_dedup,priority:2" json:"dedupKey"`
	// Action 业务事件子类型（economy_change…），供下游分流/展示。
	Action string `gorm:"type:varchar(64);not null" json:"action"`
	// NodeUUID 事件来源节点（经济多区/多节点聚合的 node→zone 维度起点）。
	NodeUUID string `gorm:"column:node_uuid;type:varchar(64);index:idx_be_node" json:"nodeUuid"`
	// InstanceUUID 事件来源实例（探针所在子服实例）。
	InstanceUUID string `gorm:"column:instance_uuid;type:varchar(64);index:idx_be_instance" json:"instanceUuid"`
	// Operator 操作者身份透传位（FR-121 业务写横切硬化时回填"哪个管理员/为什么"；自发事件为空）。
	Operator string `gorm:"type:varchar(128)" json:"operator,omitempty"`
	// PayloadJSON 业务信封原始载荷 JSON（CP 不解析语义，原样留存供下游/审计；经济域同时落结构化专表）。
	PayloadJSON string `gorm:"column:payload_json;type:text" json:"payloadJson"`
	// OccurredAt 业务侧事件发生时间（epoch 毫秒，来自探针信封；0 表示未携带）。
	OccurredAt int64 `gorm:"column:occurred_at;index:idx_be_domain_time,priority:2" json:"occurredAt"`
	// CreatedAt CP 落库时间。
	CreatedAt time.Time `json:"createdAt"`
}

// EconomyBalanceMirror 是经济结构化镜像表：每 (节点, 区, 玩家, 货币) 的**最新余额**快照（FR-122，见 ADR-028）。
//
// 由经济变更事件流（[BusinessEvent] domain=economy）按 ledgerId 去重后**按 seq 单调推进**维护：仅当来量
// seq ≥ 已存 seq 时覆盖余额，乱序/重发的旧事件不回退镜像。聚合维度 (NodeUUID, ZoneID, PlayerName, Currency)
// 唯一——**node→zone 维度**确保跨区同名玩家不串味/不重复计数（mce 账户按 zoneId 隔离，同名玩家跨区独立）。
//
// 非业务真源：余额真源仍在各服 mce 库，本表是供平台高效查询/对账的汇聚镜像（守数据所有权不变量）。
type EconomyBalanceMirror struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// NodeUUID 余额所在节点（聚合 node→zone 维度起点）。
	NodeUUID string `gorm:"column:node_uuid;type:varchar(64);not null;uniqueIndex:idx_ebm_scope,priority:1" json:"nodeUuid"`
	// ZoneID mce 区 ID（CoreLib zoneId）；跨区同名玩家独立隔离的关键维度。
	ZoneID string `gorm:"column:zone_id;type:varchar(64);not null;uniqueIndex:idx_ebm_scope,priority:2" json:"zoneId"`
	// PlayerName 玩家名（区分大小写，mce 口径）。
	PlayerName string `gorm:"column:player_name;type:varchar(64);not null;uniqueIndex:idx_ebm_scope,priority:3;index:idx_ebm_player" json:"playerName"`
	// Currency 货币 identifier（全局稳定业务键；探针侧已由 currencyId Int 折算，跨服可聚合）。
	Currency string `gorm:"type:varchar(64);not null;uniqueIndex:idx_ebm_scope,priority:4" json:"currency"`
	// CurrencyID 货币 mce Int 原值（审计回溯用；跨服不保证一致，故不作聚合键）。
	CurrencyID int `gorm:"column:currency_id;default:0;not null" json:"currencyId"`
	// Balance 最新余额（字符串承载 BigDecimal，禁浮点，防多币种精度失真）。
	Balance string `gorm:"type:varchar(64);not null" json:"balance"`
	// LastSeq 已应用的最大 mce 区内单调 seq（镜像推进游标，防乱序/重发回退余额）。
	LastSeq int64 `gorm:"column:last_seq;default:0;not null" json:"lastSeq"`
	// LastLedgerID 最近一次应用的总账流水 ID（可追溯到 [EconomyLedgerEntry]）。
	LastLedgerID int64 `gorm:"column:last_ledger_id;default:0;not null" json:"lastLedgerId"`
	// LastEntryType 最近一次变更的入账类型（DEPOSIT/WITHDRAW/…）。
	LastEntryType string `gorm:"column:last_entry_type;type:varchar(32)" json:"lastEntryType"`
	// OccurredAt 最近一次变更的业务发生时间（epoch 毫秒）。
	OccurredAt int64 `gorm:"column:occurred_at;default:0;not null" json:"occurredAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// EconomyLedgerEntry 是经济变更/操作审计表：逐条经济变更的结构化留痕（FR-122，见 ADR-028）。
//
// 与通用 [BusinessEvent] 并存——envelope 是插件无关底座，本表是经济域**结构化专表**，把信封 payload 拆列
// 供高效查询/对账（按玩家/货币/区/时间维度）。按 LedgerID 唯一去重（与 envelope 同源 dedupKey），
// 业务数据**不降采样、不丢**（区别于监控时序，ADR-028）。append-only，不随镜像覆盖。
type EconomyLedgerEntry struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// LedgerID mce 总账流水 ID（去重锚点，全局唯一）。
	LedgerID int64 `gorm:"column:ledger_id;not null;uniqueIndex:idx_ele_ledger" json:"ledgerId"`
	// NodeUUID / InstanceUUID 事件来源（node→zone 聚合维度）。
	NodeUUID     string `gorm:"column:node_uuid;type:varchar(64);index:idx_ele_node" json:"nodeUuid"`
	InstanceUUID string `gorm:"column:instance_uuid;type:varchar(64)" json:"instanceUuid"`
	// ZoneID mce 区 ID。
	ZoneID string `gorm:"column:zone_id;type:varchar(64);index:idx_ele_player,priority:1" json:"zoneId"`
	// PlayerName 玩家名。
	PlayerName string `gorm:"column:player_name;type:varchar(64);index:idx_ele_player,priority:2" json:"playerName"`
	// Currency 货币 identifier；CurrencyID 为 mce Int 原值。
	Currency   string `gorm:"type:varchar(64)" json:"currency"`
	CurrencyID int    `gorm:"column:currency_id;default:0;not null" json:"currencyId"`
	// EntryType 入账类型（DEPOSIT/WITHDRAW/CONSUME/REFUND/TRANSFER_IN/TRANSFER_OUT/ADJUST）。
	EntryType string `gorm:"column:entry_type;type:varchar(32)" json:"entryType"`
	// SignedAmount 带符号变更额（字符串承载 BigDecimal，正入负出）。
	SignedAmount string `gorm:"column:signed_amount;type:varchar(64)" json:"signedAmount"`
	// BalanceAfter 变更后余额（字符串承载 BigDecimal）。
	BalanceAfter string `gorm:"column:balance_after;type:varchar(64)" json:"balanceAfter"`
	// Seq mce 区内单调投递序号（排序/回放游标，非幂等键）。
	Seq int64 `gorm:"default:0;not null" json:"seq"`
	// OccurredAt 账务发生时间（epoch 毫秒）。
	OccurredAt int64 `gorm:"column:occurred_at;default:0;not null;index:idx_ele_time" json:"occurredAt"`
	// CreatedAt CP 落库时间。
	CreatedAt time.Time `json:"createdAt"`
}
