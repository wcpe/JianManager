# 功能规格：Bot 乱斗场景与受管稳定性压测

> 状态：已审核（用户授权代理自审，2026-07-28）　·　关联 PRD：FR-404　·　优先级：P0　·　依赖：FR-398、FR-399　·　分支：dev

## 1. 背景与目标

现有压测可建立分布式 Bot 连接并采集资源，但 `attack_until` 在 Bot 死亡后不会自动重生，且 MCP 创建运行不能提交 Scenario V2。因此无法对「50 个 Bot 在隔离地图持续寻路、攻击附近玩家和敌对生物、死亡后重生再战」进行一小时的可重复稳定性验证。

本 FR 补齐最小的场景执行能力与 MCP 入口，并使用受 JianManager Worker 管理的 Paper 实例和既有嵌入 ServerProbe 采集资源稳定性证据。

## 2. 需求与范围

### 2.1 范围内

1. `attack_until` 可选启用自动重生恢复：Bot 死亡后停止当前寻路，调用一次 Mineflayer `respawn()`，等待新的 `spawn` 事件后清除锁定目标并继续同一攻击动作。
2. `attack_until` 支持混合候选目标和确定性随机挑选：现有 `types` 仍为 OR 匹配；新增 `priority: "random"`。一小时验收场景以 `types` 同时包含玩家与敌对生物的真实 Mineflayer 类型。
3. `attack_until` 可选 `searchArea`：周围无目标时在指定半径/航点内确定性寻路搜敌；发现候选目标即切换追击。未配置时保持既有「空窗超时失败」语义。
4. `attack_until.stop.minClientAttackAttempts` 可选设置本地攻击活跃度下限。到动作截止时未达到该下限，动作以 `ATTACK_ACTIVITY_UNMET` 失败；达到时只证明 Mineflayer 客户端已调用攻击动作。
5. MCP `loadtest_run_create` 新增可选结构化 `scenario` 参数，直接复用 HTTP 创建会话的解析、冻结、权限、预检和分片链路。
6. 修正场景总截止时间：持续动作在其自身 `durationMs` 恰好到期时必须先结算，而不是被全局截止抢先标为 `ACTION_CANCELLED`。
7. 一小时真机压测使用受管隔离 Paper：部署内嵌 ServerProbe，采集 TPS、MSPT、JVM heap、线程、CPU、在线数和世界实体数；目标进程树资源继续由 FR-399 采样。

### 2.2 范围外

- 不新增 Bot 进程、数据库表、gRPC/IPC 协议或新的外部依赖。
- 不把 ServerProbe 扩展为伤害、击杀、死亡的可信业务事件源；本 FR 不声称服务端确认过每次攻击、PVP 伤害或击杀。
- 不改现有未配置新字段的 Scenario V2 行为。
- 不把本次 50 Bot 结果称为 TEST-500 或 500 Bot 容量结论。

## 3. 设计

### 3.1 `attack_until` 加性字段

```json
{
  "type": "attack_until",
  "selector": { "types": ["player", "zombie", "skeleton"], "radius": 24, "priority": "random" },
  "chase": true,
  "reacquire": true,
  "searchArea": { "type": "radius", "center": { "x": 0, "y": 64, "z": 0 }, "radius": 24 },
  "respawn": { "maxAttempts": 1000, "retryBackoffMs": 500, "timeoutMs": 10000 },
  "attackIntervalMs": 600,
  "stop": { "durationMs": 3600000, "minClientAttackAttempts": 1 }
}
```

- 所有新增字段均可选。未提供 `respawn`、`searchArea`、`minClientAttackAttempts` 或 `priority:"random"` 时，兼容现有动作行为。
- `respawn.maxAttempts` 限制累计重生尝试，范围为 1–10000；达到上限返回 `RESPAWN_LIMIT_EXCEEDED`。`retryBackoffMs` 和 `timeoutMs` 必须为有界正数。
- 死亡恢复只能由场景单一 250ms Fleet tick 驱动，不新增每 Bot 定时器。恢复期间不得重复调用 `respawn()`；新 spawn 到达后重置锁敌、搜敌、追击和死亡等待状态。
- `priority:"random"` 以场景 seed、bot ordinal 与候选实体 ID 产生可重复排序，不使用全局随机数。
- `searchArea` 复用现有区域模型与 Pathfinder；路径不可用/失败沿用结构化 `PATHFINDER_UNAVAILABLE` / `PATH_NOT_FOUND`。
- 终态结果增加并保留 `clientAttackAttempts`、`respawnCount`、`reacquireCount` 与 `pathfinderGoals`。它们是客户端活动证据，不是服务端伤害证据。

### 3.2 MCP 与既有场景链路

`loadtest_run_create` 的 input schema 和 direct-create 分支增加可选 `scenario: object`，将原始 JSON 传入既有 `CreateBotStressSessionRequest.Scenario`。不新增 MCP 工具，不绕过 `ParseScenarioV2`、会话冻结、目标/执行节点 scope 或预检计划令牌。

### 3.3 受管目标与保护

- 目标 Paper 必须由 JianManager Worker 受管，以便部署 ServerProbe、采集 Probe 指标和目标进程树资源；不得复用业务服或上次临时容器。
- 测试服独立世界、端口、工作目录和数据根，`online-mode=false`、`pvp=true`、`spawn-protection=0`；地图与怪物密度受限，禁止无限制刷怪。
- 目标 JVM 上限 8GiB；真机压测编排器以主机 `MemAvailable < 8GiB` 立即停止 Bot 会话。Probe 断链、Worker 不可用、连续 60 秒连接率低于 95%、连续 3 个样本 TPS<18 或 MSPT>50ms 同样停止并保留证据。这是本次验收编排的保护规则，不新增平台级阈值状态机。
- `doImmediateRespawn` 与 Mineflayer `respawn()` 的实际交互必须在真机先做小规模验证；若不兼容，以明确失败记录交付，不静默降级。

## 4. 任务拆分

- [ ] 为 Scenario V2 的新增字段、范围校验与规范化快照补失败测试。
- [ ] 为 Bot Worker 的死亡→单次重生→spawn→恢复攻击、重生失败上限、随机目标和搜敌补测试。
- [ ] 实现 `attack_until` 的加性恢复、搜敌和本地攻击活跃度。
- [ ] 修复持续动作的截止结算，并补 Control Plane 回归测试。
- [ ] 为 MCP `loadtest_run_create.scenario` 透传补测试并实现。
- [ ] 创建受管隔离 Paper、部署 ServerProbe，并完成小规模重生兼容验证。
- [ ] 运行 1 Worker × 50 Bot × 1 小时乱斗压测，导出指标、报告、动作结果和日志。
- [ ] 文档同步：PRD、ARCHITECTURE、API、CHANGELOG。

## 5. 验收标准

1. 未使用新增字段的现有 Scenario V2 测试与运行行为保持兼容。
2. 自动化测试证明：死亡时不会重复 `respawn()`；收到新 spawn 后恢复同一 `attack_until`；超出上限报 `RESPAWN_LIMIT_EXCEEDED`；随机选目标可复现；搜敌与截止结算均可验证。
3. MCP 传入合法 `scenario` 可创建 `scenario_v2` 会话；非法字段被既有场景校验拒绝；无 `scenario` 的既有调用不变。
4. 真机目标是受管隔离 Paper，ServerProbe 已加载且持续可采 TPS、MSPT、JVM heap、线程、CPU、在线数和实体数。
5. 50 Bot 在单 Worker 容量 50 的前提下运行满 1 小时；每个 5 秒样本连接率至少 95%，无 Worker/Bot Worker 崩溃，未触发 8GiB 内存守卫。
6. 终态动作结果证明每个成功完成的 Bot 至少有一次 `clientAttackAttempts`；真机预检故意制造至少一次死亡并确认重生恢复。该标准不等同于服务端伤害、击杀或 PVP 成功。
7. 产出 JSON 报告、原始 metrics/events、ServerProbe 指标、目标与 Worker 日志，并在交付前由用户确认真机证据。

## 6. 风险与待定

- Mineflayer 在目标协议版本中玩家实体的实际 `type` 值必须在小规模真机预检记录；不匹配时需调整验收场景，而不是放宽 selector。
- 50 个路径规划和近战行为会显著高于此前 idle 负载；CPU/TPS 保护可能提前停止测试，属于有效容量结论。
- ServerProbe 资源指标不提供可信战斗事件；任何“击杀数”“伤害量”的业务结论均不在本 FR 的证据范围。
