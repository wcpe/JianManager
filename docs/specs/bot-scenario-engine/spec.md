# 功能规格：可验证 Bot 场景引擎与塔防预设

> 状态：已审核（2026-07-18）　·　关联 PRD：FR-352（增强 FR-274）　·　计划分支：feature/fr-352-bot-scenario-engine
> 超级规格：`../bot-load-platform/super-spec.md`　·　HTTP API：`../bot-load-platform/api.md`
> 依赖：FR-351 开发中；FR-352 实现与其已冻结的分布式契约协同，完成状态以 PRD 为准

## 1. 背景与目标

FR-274 的 orchestrated 行为按时长切换 idle/follow/patrol/guard/custom，但不能可靠表达“等待登录→输入命令→确认进房→全部准备后同时开局→确认抵达→锁敌追击并持续平A”。现有 move 只设置 goal 不等待到达，custom attack 只执行一次或每 tick重找最近目标，命令也只有已发送事件。

本 FR 建 V2 结构化场景和可验证动作状态机。Control Plane 负责 YAML/JSON 单点解析、校验、cohort 分配和外部协调；Bot Worker 负责本地 Mineflayer 动作；需要外部事实的动作进入等待态，只消费经 CP 路由的关联信号。本 FR 不实现信号数据源，当前规划中的 FR-358 也不负责 ServerProbe/ProbeEvent 接入；该数据源仅可由未来独立、可选的适配能力提供。

## 2. 需求（要什么）

- 内部规范格式为 Scenario V2 JSON，YAML 仅作为编辑/导入格式。
- 支持多个 cohort，百分比合计 100，按 seed 和 Bot 稳定序号确定性分配。
- 支持动作：wait_spawn、roam_in_area、send_command、wait_probe_event、barrier、move_to_and_wait、find_entity、attack_until、wait、respawn_and_rejoin。
- 每个动作有稳定 stepId、timeout、重试、退避、取消、resumePolicy 和结构化结果。
- 支持同步屏障：全部、固定数量或百分比满足后统一释放。
- 主城漫游支持半径区域和航点区域，目标点可重现、不会走出边界。
- move_to_and_wait 必须确认实际进入半径并稳定，不得 setGoal 后立即推进。
- attack_until 支持目标筛选、锁定、追击、普通攻击节奏和停止条件。
- 探针等待动作不让 Bot Worker 直连 CP；通过 Worker IPC 信号完成。
- 旧 V1 orchestrationYaml 可转换为兼容 V2 snapshot，旧字段仍保留。
- 提供内置“塔防核心链路”模板数据，不包含前端模板管理。

**范围内**：V2 schema/parser/validator、V1 转换、cohort 分配、CP barrier/外部等待协调、gRPC/IPC 信号使用、Bot Worker 动作引擎、动作结果持久化、塔防预设、自动化测试与文档。

**不做**：ServerProbe/塔防插件事件数据源（未来独立可选适配能力，不归当前 FR-358～361）、自动重连和进程恢复（FR-354）、负载曲线/verdict（FR-359）、UI（FR-360/361）、技能/放塔/商店/复杂背包、任意脚本表达式。

## 3. 设计（怎么做）

### 3.1 Scenario V2

严格采用共享 `../bot-load-platform/api.md` §2.2 的 `BotLoadScenarioV2` / `BotLoadScenarioAction` 判别联合；超级规格 §8 仅属于 FR-358 的 command_schedule，不再作为 Scenario schema 真源。Go 内部定义显式 DTO，禁止 `map[string]any` 贯穿业务层；动作的可变参数可在解析后落各自结构体。YAML/JSON 都先解析到同一 DTO，再执行同一 validator。

校验规则：

- version 必须为 2；seed 必填 int64。
- cohorts 1..20；key 匹配 `[a-z][a-z0-9-]{0,63}` 且唯一。
- percent 为整数 1..100，总和恰为 100。
- 每 cohort steps 1..100；step id 匹配同样规则并在 cohort 内唯一；用于可判定运行的 V2 场景每个 cohort 必须恰有一个持续动作标 `observationStep:true`。
- timeoutMs 默认按动作给定，范围 100..3_600_000。
- maxAttempts 默认 1，范围 1..10；retryBackoffMs 0..60_000。
- Scenario V2 模板变量只允许当前实现白名单：`botName`、`botUuid`、`runId`、`cohortKey`、`correlationToken`，内部塔防预设构建期还可绑定 `roomKey`；公开输入中的未知 `{{...}}` 返回 path 级错误。FR-358 `command_schedule` 的 `botOrdinal/actionRunId` 白名单是独立契约，不扩展 Scenario V2。
- command 长度 1..256；禁止换行和 NUL，不禁止 `/`。
- 坐标必须有限数值；radius 0.5..256。
- attackIntervalMs 100..5000；entity types 最多 32；evidenceWindowMs 1000..300000，minDamageEventsPerWindow 1..100000；durationMs 必须覆盖至少一个完整 evidence window。
- respawn_and_rejoin 引用的入口 step 必须存在且不得形成无界递归。

### 3.2 V1 转换

- 每个旧会话生成单 cohort `legacy`，percent=100。
- orchestrated phase：按 behavior 映射 wait/roam/guard 兼容动作；无法确定完成语义的 follow/patrol 保持 `legacy_behavior` 内部兼容适配，不暴露给新建 V2 场景。
- custom chat→send_command（无 `/` 时仍聊天）；wait→wait；move→move_to_and_wait；attack→attack_until（duration=阶段剩余时长）；interact/use_item 仅走 legacy adapter。
- 原始 OrchestrationYAML 不覆盖；ScenarioSnapshot 保存转换结果。
- 转换失败时旧会话仍可走原 orchestrated 行为，不破坏历史。

### 3.3 Cohort 分配

实现纯函数：输入 seed、botCount、按声明顺序的 cohort percentage；输出每个 Bot ordinal→cohortKey。

- 先计算 floor(count*percent/100)。
- 余数按声明顺序分配。
- cohort 内 Bot ordinal 稳定；重试/恢复不重新抽签。
- 结果写 Bot.CohortKey，并把 cohort 对应 scenario 子树下发。

### 3.4 动作执行器

Bot Worker 每 Bot 只允许一个 ScenarioRunner；Runner 管理当前 step、attempt、deadline、cancel token 和已锁定实体。

接口语义：

```ts
interface ScenarioAction {
  start(ctx: ActionContext): Promise<ActionStartResult>
  tick(ctx: ActionContext, now: number): Promise<ActionTickResult>
  signal?(ctx: ActionContext, signal: ActionSignal): Promise<ActionTickResult>
  cancel(ctx: ActionContext, reason: string): Promise<void>
  dispose(): Promise<void>
}
```

- start/tick 不抛异常控制正常流程；异常统一转 ACTION_INTERNAL_ERROR。
- 任何终态只发一次 action-event。
- step 切换前必须 dispose 旧动作、取消 pathfinder goal 和 timer。
- Runner 不为每 Bot 建无界 setInterval；复用现有集中 Tick 或单一调度器。

### 3.5 动作语义

#### wait_spawn

监听 spawn；若创建 Runner 时已 spawn，立即成功。end/kicked 前未成功则失败 CONNECT_ENDED。

#### roam_in_area

- radius：以中心为基准，用 seed+botOrdinal+stepId 生成确定性伪随机目标。
- waypoints：在声明航点间按确定性顺序循环。
- 每次目标必须位于区域内；抵达后按 pauseMs 范围确定性暂停。
- 单目标连续 N 次路径失败（默认 3）才使动作失败；偶发重规划不失败。

#### send_command

- 展开白名单变量并调用 bot.chat。
- 发 action result 只表示发送成功；若业务需要确认，模板必须紧跟 wait_probe_event。
- 自动生成 correlationToken 并放入 ActionContext，后续步骤可复用。

#### wait_probe_event

- start 后发 waiting external action-event，不轮询网络。
- 仅接受 runUuid(sessionUuid)/botUuid/actionRunId/correlationToken/eventType 全匹配的 signal；数字 runId 不进入 Fleet/SignalBotActions 身份字段。
- 重复 signal 幂等；迟到 signal 只做诊断不改变已终态动作。

#### barrier

- Bot Worker 上报 barrier-arrived 后等待 CP `barrier-release`。
- barrier 唯一作用域为 `runUuid+stageIndex+cohortKey+barrierKey+round`；跨进程字段使用 proto `session_uuid`。进入 stage 时冻结 expectedBotSet，分母不因断线、失败、停止或重分配缩小。
- CP BarrierCoordinator 维护冻结的期望集合、到达集合、释放状态和 deadline。
- release type：all/count/percent；percent 按 ceil(expectedBotSet*value/100)。
- 释放信号含 releaseAtUnixMs/round，Worker 在本地等到同一时间，允许 250ms 时钟误差。
- 释放后重连 Bot 若该 round 已释放则直接越过 barrier 进入下一步，不再次计数；未释放前重连只允许当前 generation 到达一次。
- 超时策略可 fail 或 release-arrived；默认 fail。

#### move_to_and_wait

- 只在目标变化/路径失败时 setGoal，不每 250ms 重设相同 goal。
- 距离≤radius 且连续 500ms 保持只表示 `localArrived=true`。
- 配置 `requireProbeEvent=area_arrived` 时，Runner 随后等待同 run/token/player/areaId 的可信信号，只有本地到达+探针到达都满足才 succeeded；严格 arrival 阈值必须使用此模式。
- pathfinder goal_reached 可加速判断，但最终本地位置仍需确认。
- pathfinder 初始化/无路径/超时给结构化错误。

#### find_entity

- selector 支持 kind、types、nameRegex（最长 128、编译失败校验拒绝）、radius、priority=nearest|lowest_health。
- 锁定 entity id；实体死亡/消失前不因出现更近目标切换。

#### attack_until

- 若未锁定目标，按 selector 找目标；chase=true 时移动到可攻击距离。
- attackIntervalMs 控制普通攻击节奏，不按 250ms tick 无脑攻击。
- 目标消失/死亡后按配置 reacquire；最大空窗超时失败 TARGET_NOT_FOUND。
- stop 支持 durationMs（截止/覆盖时长）、damageAtLeast、killsAtLeast、probeEvent、evidenceWindowMs+minDamageEventsPerWindow；successPolicy=any|all 只作用于可信条件，durationMs 本身不属于成功条件。
- Mineflayer attack 调用只计 clientAttackAttempts；可信 damage/kills 仅由完整关联的外部 signal 累加。当前 FR-358 不提供该信号数据源，未来独立可选适配器可接入。
- 截止时可信条件未满足则 `failed/ATTACK_ASSERTION_UNMET`，不新增 completed_unverified 状态。
- 严格长稳塔防预设使用 durationMs=3600000、successPolicy=all、30秒 evidence window，每个完整窗口至少一条可信 damage/kill；首次伤害不能提前结束动作。
- evidence window 以 CP 发出的 `observation-start` 信号统一对齐；运行观察窗结束时发 `observation-complete`，Runner 仅在全部窗口满足时 succeeded，否则 failed/ATTACK_ASSERTION_UNMET。

#### respawn_and_rejoin

- 等待 respawn/spawn 后跳转到引用入口 step；最多重进次数由 maxAttempts 控制。
- 不能绕过 run 总截止时间。

### 3.6 动作事件与持久化

使用 FR-351 已铺的 `action-event IPC → StreamBotFleetEvents(action_event) → CP` 类型化链路。CP ActionResultService：

- action-start 以 actionRunId upsert running。
- 终态按 actionRunId 条件更新；重复终态忽略。
- 保存 result_json，但限制 16KiB，超出截断并标 truncated。
- 错误码固定枚举：CONNECT_TIMEOUT、CONNECT_ENDED、PATHFINDER_UNAVAILABLE、PATH_NOT_FOUND、MOVE_TIMEOUT、TARGET_NOT_FOUND、PROBE_EVENT_TIMEOUT、BARRIER_TIMEOUT、ACTION_CANCELLED、ACTION_INTERNAL_ERROR。

### 3.7 外部信号路由

本 FR 基于 FR-351 已完成的 `SignalBotActions` gRPC→IPC 逐项回执层，实现通用 ActionSignalRouter，不实现探针事件来源：

- 输入：runUuid（映射 proto `session_uuid`）、botUuid/actionRunId、correlationToken、type、payload；数字 runId 仅用于数据库/API 引用。
- 查询等待中的 action result 和 Bot.ExecutorNodeID。
- 按执行节点分组调用 SignalBotActions。
- Bot Worker 校验 generation/actionRunId 后投递 Runner。
- barrier/stop 等平台内部信号由后续负载编排 FR 接入；ServerProbe/ProbeEvent 不归当前 FR-358～361 所有，仅允许未来独立可选适配器接入。

### 3.8 塔防预设

内置 preset key `tower-defense-core-v1`，内容符合用户确认链路：

- lobby cohort：wait_spawn + roam_in_area。
- combat cohort：wait_spawn + send_command + wait room_joined + barrier + wait game_started + move_to_and_wait + attack_until。
- 默认比例 20/80，但创建时可改；不硬编码命令、坐标、怪物类型，预设参数必须由用户填写。

## 4. 任务拆分

- [ ] 测试先行：V2 YAML/JSON parser、path 错误、模板变量、动作参数边界。
- [ ] 测试先行：V1→V2 转换与旧会话回退。
- [ ] 测试先行：cohort 确定性分配、余数和恢复稳定性。
- [ ] Go：场景 DTO/validator/summary/snapshot 与 ActionResult model/service。
- [ ] Go：BarrierCoordinator、ActionSignalRouter 和 gRPC signal 调用。
- [ ] Bot Worker：建立固定测试入口——`test/run-tests.mjs` 用 Node 内置 `node:test.run()` 递归收集 `test/**/*.test.mjs`；`npm test` 固定执行 `npm run build && node test/run-tests.mjs`；Taskfile 增 `bot:test` 并把它加入顶层 `task test`。不新增依赖。
- [ ] Bot Worker：ScenarioRunner、动作接口、统一调度和 action-event。
- [ ] Bot Worker：wait_spawn/roam/send/wait_probe/barrier/move/find/attack/wait/respawn 动作及测试。
- [ ] Fake 能力：spawn/kick/signal/虚拟实体接口；真实寻路/伤害仍留真机。
- [ ] 塔防核心预设和兼容 orchestrated adapter。
- [ ] 文档同步：ARCHITECTURE 场景链路、API 场景 schema、PRD 本 FR 状态、CHANGELOG。

## 5. 验收标准

### 自动化

- [ ] 所有非法 schema 返回稳定 path/message，JSON/YAML 同语义。
- [ ] 500 Bot cohort 分配总数精确、同 seed 可重现、重试后 cohort 不漂移。
- [ ] move_to_and_wait 未抵达时绝不推进；抵达稳定 500ms 后成功。
- [ ] barrier 99%/all/count、迟到、重复、超时和统一 releaseAt 全覆盖。
- [ ] wait_probe_event 只接受完整关联信号，错 run/token/action 不误完成。
- [ ] attack_until 锁定目标、追击、攻击节奏、目标死亡重选和可信 damage/kill stop 全覆盖。
- [ ] Runner cancel/dispose 后无遗留 timer/pathfinder goal；长循环内存不增长。
- [ ] 旧 orchestrationYaml 和旧无编排 behavior 回归全绿。
- [ ] `npm test`、`task bot:test`、顶层 `task test` 均真实执行 bot-worker Node 单元/IPC/Fake-MC 测试并全绿；Go 相关 tests/race、bot-worker build/lint 全绿。

### 真机（FR-352 必需）

- [ ] Bot 在真实 MC 主城半径区域持续漫游，不越界、不原地抖动；`move_to_and_wait` 绕障并确认稳定抵达后才推进。
- [ ] 通用 `send_command` 在真实连接中调用 `bot.chat`，同步抛错与未抛错结果符合 ADR-075；不要求服务端业务回执。
- [ ] 500 Bot 使用不依赖业务插件的 Scenario V2 完成确定性 cohort、通用屏障和取消/超时链路，达到阈值后在 1 秒窗口内释放。
- [ ] `find_entity/attack_until` 在真实实体环境中验证锁定、追击、攻击节奏、目标消失重选和取消；没有外部可信伤害信号时不得把 client attack 当成 damage/kills 成功证据。
- [ ] `respawn_and_rejoin` 在不依赖房间插件的普通死亡/重生环境中按配置恢复入口，且不越过运行总截止时间。

### 真机（可选 legacy，非 FR-352 交付门禁）

仅当未来独立 ServerProbe/业务适配器实际提供关联信号时，附加验证 `room_joined`、`area_arrived`、可信 damage/kill、塔防开局和完整死亡重进业务链。缺少该数据源必须记为 `not_applicable`，不得阻塞 FR-352 完成。

## 6. 风险 / 待定

- Mineflayer/pathfinder 在不同 MC 版本行为可能不同，真机必须覆盖目标版本。
- 实体类型名由 Mineflayer registry 决定；模板校验只能做字符串边界，真实存在性在运行时判断。
- barrier 依赖节点时钟；要求 NTP，同步误差验收≤250ms。
- probe 信号仅使用 fake router 验证关联、超时和幂等；真实 ServerProbe/ProbeEvent 数据源不属于当前 FR-358～361 验收范围，未来独立可选适配器落地后另行联合验收。
- 不新增 YAML/状态机依赖，复用现有 yaml.v3 和项目代码。
