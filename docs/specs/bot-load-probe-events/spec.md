# 功能规格：ServerProbe 压测断言桥与塔防事件适配

> 状态：已审核（2026-07-18）　·　关联 PRD：FR-353（增强 FR-065/066）　·　计划分支：feature/fr-353-bot-load-probe-events
> 超级规格：`../bot-load-platform/super-spec.md`　·　HTTP API：`../bot-load-platform/api.md`
> 依赖：FR-352 已落协调分支　·　可与 FR-354 并行

## 1. 背景与目标

Mineflayer 能证明命令发送、位置变化和 attack 调用，但不能可靠证明玩家进入了正确房间、游戏真正开始、攻击造成了伤害或怪物被击杀。用户允许修改塔防插件，因此正式压测必须由塔防插件发布结构化测试事件，经 ServerProbe 既有反向 WS 桥上报，并关联到具体 run/Bot/action。

本 FR 不新增通信通道：复用 ADR-016 的 ServerProbe→本机 Worker→gRPC PluginEvent→CP 链路，使用现有 PluginEvent 的 domain/dedup_key/request_id/raw_json 字段。

## 2. 需求（要什么）

- ServerProbe 提供最小、可选的 Bukkit 本地发布 SPI，塔防插件 compileOnly 集成。
- 塔防插件发布目标子服/房间/开局/波次/伤害/击杀/死亡/重生/结束事件。
- 代理跨服事件优先复用 ServerProbe 已有 cross_server 事件，CP 标准化为 target_server_entered。
- 所有压测事件使用 `domain=bot_load`，带 eventId/runId/correlationToken。
- CP 按 eventId 幂等持久化，按 run/token/player 关联 Bot/action。
- 可信事件经 FR-352 ActionSignalRouter 路由到执行 Bot 的 Worker。
- 重复、迟到、乱序、未知和无法关联事件可诊断但不得误推动动作。
- 探针断线时塔防插件业务不得被阻塞；事件发布为非阻塞、有界队列。
- 未安装 ServerProbe 或未启用 load-test bridge 时，塔防插件正常运行。

**范围内**：ServerProbe API/SPI、塔防插件事件信封契约、ServerProbe 桥转发、CP ingest/去重/关联/持久化、已有 cross_server 标准化、测试与文档。

**不做**：修改场景动作（FR-352）、Bot 恢复（FR-354）、指标/verdict（FR-355）、聊天/计分板文本解析、让塔防插件直连 Worker/CP、为普通玩家事件全量落 bot_load 表。

## 3. 设计（怎么做）

### 3.1 ServerProbe 发布 SPI

塔防房间/战斗逻辑位于 Bukkit 后端，因此新 API 只要求 Bukkit ServicesManager；Bungee/Velocity 跨服事实复用既有 ServerProbe cross_server。

ServerProbe jar 暴露最小接口（包名按 ServerProbe 现有命名落地时对齐，不另起依赖）：

```java
public interface LoadTestEventPublisher {
    boolean isAvailable();
    void publish(LoadTestEvent event);
}

public final class LoadTestEvent {
    String eventId;
    String type;
    String playerName;
    UUID playerUuid;
    String runId;
    String correlationToken;
    String roomId;
    String gameId;
    Map<String, Object> data;
    long occurredAtEpochMillis;
}
```

- ServerProbe enable 时向 Bukkit ServicesManager 注册 publisher；disable 时 unregister。
- 塔防插件 `softdepend: [ServerProbe]`，通过 ServicesManager 查找；不存在即跳过测试事件，不影响玩法。
- DTO 不接受任意对象递归；data 仅 JSON 标量/数组/对象，序列化后最大 16KiB。
- publish 不做网络阻塞，只入 ServerProbe 有界队列。

塔防插件另注册只读 `LoadTestCapabilityProvider`：

```java
public interface LoadTestCapabilityProvider {
    LoadTestCapabilities snapshot();
}
```

snapshot 至少包含 adapterId/version、支持事件类型、加入命令模板能力，以及 area registry：`areaId/roomPattern/world/min/max/stableMillis`。ServerProbe 把它并入既有 QueryServerState `state_json.botLoadCapabilities`；CP preflight 必须验证场景引用的 areaId/事件类型存在。场景中的坐标只是 Mineflayer 导航提示，服务端可信抵达以 area registry 为准。

### 3.2 塔防插件适配

塔防插件在明确业务事实发生后发布，不从日志反推：

| 事件 | 触发点 | 必填 data |
|---|---|---|
| room_joined | 玩家真正加入房间数据结构后 | roomId |
| room_ready | 房间就绪人数变化且达到可开局条件 | roomId, ready, expected |
| area_arrived | 服务端确认玩家进入模板声明区域并稳定满足判定 | roomId, areaId, x, y, z |
| game_started | 游戏状态切为 RUNNING 后 | roomId, gameId |
| wave_started | 波次状态切换后 | roomId, gameId, wave |
| damage_dealt | 伤害最终结算后 | targetType, targetId, damage |
| monster_killed | 击杀归属确认后 | targetType, targetId |
| player_died | 房间内死亡事实成立 | roomId, gameId |
| player_respawned | 重生完成并恢复可操作后 | roomId, gameId |
| game_ended | 结算完成后 | roomId, gameId, result, durationMs |

runId/correlationToken 来源：塔防测试加入命令接收 `{{correlationToken}}`，插件把 token 与玩家/房间绑定；token 缺失的普通玩家不发布 bot_load 事件。运行结束/玩家退出/超时后清理绑定。

### 3.3 代理跨服标准化

- 既有 cross_server PluginEvent 带 player/from_server/to_server。
- CP 若 active Bot 当前等待 `target_server_entered` 且 to_server 匹配场景期望，则构造 `source=cross_server` 内部标准事件；eventId 使用固定 namespace 对 `instanceUUID|playerUUID/name|from|to|原事件timestamp` 计算 UUIDv5，稳定可去重且不冒充塔防插件发布 UUID。
- 若塔防代理插件能携 correlationToken，则优先使用 token；否则仅可按 run 内唯一 playerName + 目标 server 关联。
- 单服多房间不使用 cross_server。

### 3.4 ServerProbe 队列与桥信封

- ServerProbe 有界队列默认 4096；满时丢最旧非关键 telemetry。关键事件（room_joined/area_arrived/game_started/wave/damage聚合/kill/death/respawn/end）优先；若仍满，累计 dropped 计数并 WARN 中文日志。
- WS 断线期间队列最多保留 30 秒或 4096 条；重连后按原 eventId 重发，CP 去重。
- Worker 对 `domain=bot_load` 建独立 30 秒/4096 条重放缓冲；新的 StreamPluginEvents 订阅先重放再实时。订阅队列满时主动断开慢流，禁止沿用通用事件的静默丢帧；CP 重连后由缓冲补发。
- damage_dealt 可按 playerUuid+correlationToken+eventType 250ms 合并累计值；CP 关联等待动作后再映射 actionRunId。其他关键事件不采样。
- 转换为既有 PluginEvent：
  - domain=`bot_load`
  - type=LoadTestEvent.type
  - dedup_key=eventId
  - request_id=correlationToken
  - timestamp=occurredAtEpochMillis
  - player_name/player_uuid/instance_uuid/server/platform 使用既有字段
  - raw_json=版本化信封（必含 occurredAtUnixMs）
- bot_load 可靠重放不新增 proto checkpoint 字段；重复由 eventId 去重。

### 3.5 CP Ingest

新增独立 `BotLoadProbeEventService`，从 PlayerEventService 的 PluginEvent 分流注入，避免混入玩家名册或 JBIS BusinessEventService。

流程：

1. domain 非 bot_load → 原链路。
2. 校验 schemaVersion/eventId/type/occurredAt/大小；非法事件 WARN 并计数，不 panic。
3. 事务 `INSERT ... ON CONFLICT(event_id) DO NOTHING`，同时写 receivedAt/matchState。
4. 解析 runId；运行不存在/已终态仍保留事件并写 unmatchedReason/late。
5. 用 correlationToken + playerName/UUID 找当前等待 action；必须唯一。
6. 通过条件更新把 `consumed_action_run_id` 从 NULL 绑定到唯一 actionRunId；更新失败表示已被消费，不再投递。
7. 匹配成功，调用 FR-352 ActionSignalRouter；路由失败保留 matched 状态供有界重试，不重复写事件。
8. 更新动作可信计数（area arrival/damage/kills/death/respawn）。

### 3.6 乱序与迟到

- 事件事实时间使用 occurredAt，接收时间只用于诊断。
- 同 actionRunId 的事件按事实时间累计，但动作已终态后仅记录 late=true，不改变结果。
- game_started 早于 room_joined 到达时：两个等待步骤分别按类型/token 查已持久化未消费事件；进入步骤时允许消费最近未消费匹配事件，避免网络乱序导致假超时。
- 每事件最多被一个 action 消费；使用超级规格已冻结的 consumedActionRunId/consumedAt/matchState/late 字段和条件更新保证原子性。

### 3.7 MSPT p95 指标链

严格判定不得从 MSPT avg 推导 p95。本 FR 在 ServerProbe 直接基于最近 60 秒原始 Tick 时长计算并暴露：

```text
serverprobe_mspt_seconds{quantile="p95",window="60s"}
```

- 刷新频率与现有 metrics scrape 对齐；值单位秒。
- Worker `ProbeSnapshot`/解析器增加 MSPTP95Millis，并把 FR-351 已预铺的 proto/心跳字段接真；本 FR 不重新生成或改名 fleet proto。
- CP 实例指标和 BotLoadMetricSample 原样保存 p95；FR-355 不二次计算。
- 老探针无该指标时为 null；严格运行连续缺失超过 60 秒按 PROBE_METRICS_MISSING 失败。
- ServerProbe 以固定 Tick 向量测试 nearest-rank p95，Worker 解析测试覆盖 avg/p95 并存与 p95 缺失。

### 3.8 安全与隔离

- SPI 只在同 JVM，本身不承担鉴权；只有带 active test token 的玩家事件进入 bot_load。
- correlationToken 随机 UUID，无权限含义；不可代替 plugin-bridge token。
- raw_json 不允许任意玩家聊天、物品 NBT 或隐私数据。
- 塔防插件发布失败不得影响主游戏事务；调用在业务提交后执行。

### 3.9 子模块工作流

ServerProbe 当前为 git 子模块。实现时：

- 先按仓库网络规则初始化子模块；不得在父仓直接伪造其文件。
- ServerProbe 改动在子模块仓独立中文 Conventional Commit；父仓 FR 分支只更新子模块指针和 JianManager 代码/文档。
- 不自动 push 子模块；若无法推送远端，状态标 partial 并提供 commit SHA/补丁证据。
- 塔防插件在独立仓库的适配提交属于真机验收外部交付物，不混入 JianManager 父仓提交。

## 4. 任务拆分

- [ ] 测试先行：LoadTestEvent DTO 校验、大小限制、队列优先级/满载/断线重发。
- [ ] ServerProbe：Bukkit ServicesManager publisher + bridge 转换 + 中文日志。
- [ ] Worker plugin bridge：bot_load 独立重放缓冲、慢流断开和重连补发测试。
- [ ] ServerProbe/Worker/CP：MSPT p95 产生、解析、additive proto/心跳和缺失语义。
- [ ] 测试先行：CP schema 校验、eventId 去重、run/token/player 关联、原子单次消费、乱序/迟到/未知事件。
- [ ] CP：BotLoadProbeEvent model/AutoMigrate/service，接 PlayerEventService 分流。
- [ ] CP：接 FR-352 ActionSignalRouter，可信 damage/kill/death/respawn 累计。
- [ ] 代理 cross_server 标准化与测试。
- [ ] 塔防插件：发布适配器（独立仓库）与房间/战斗事件测试。
- [ ] 集成测试：fake plugin bridge→Worker StreamPluginEvents→CP ingest→SignalBotActions。
- [ ] 文档同步：ARCHITECTURE、API、ServerProbe/塔防接入说明、PRD 本 FR 状态、CHANGELOG。

## 5. 验收标准

### 自动化

- [ ] 同 eventId 重放 10 次只落一条，只推动一次动作。
- [ ] 错 run/token/player/type 的事件不推动动作且可在诊断中看到 unmatched 原因。
- [ ] game_started 与 room_joined 乱序仍能被正确步骤消费；已终态动作不被迟到事件翻转。
- [ ] ServerProbe WS 断开和 Worker→CP StreamPluginEvents 断开后均能重放关键事件，CP 去重无重复计数；慢消费者不静默丢 bot_load 关键事件。
- [ ] 4096 队列满时有明确 dropped 指标/日志，关键事件优先/慢流断开策略测试通过。
- [ ] area_arrived 只有服务端确认区域和 token 匹配才推动 move 动作。
- [ ] MSPT p95 固定 Tick 向量、Worker 解析、老探针缺失语义测试通过，禁止 avg 冒充。
- [ ] 普通玩家无 test token 时不产生 bot_load 事件。
- [ ] 事件 payload 超 16KiB/非法 JSON/未知 schema 不导致崩溃。
- [ ] Go tests/race、ServerProbe tests/build 全绿；父仓子模块指针可复现。

### 真机

- [ ] 代理网络：Bot 从 Lobby 切到塔防子服，cross_server/target_server_entered 正确关联。
- [ ] 单服房间：加入命令后只有真正进入房间才收到 room_joined。
- [ ] 游戏开始、波次、伤害、击杀、死亡、重生、结算事件与游戏内事实逐项对拍。
- [ ] 暂停 Worker plugin-bridge 网络再恢复，关键事件重发且运行不重复计数。
- [ ] 移除/禁用 ServerProbe 时塔防插件正常运行，仅压测可信断言不可用并被 preflight 阻断。

## 6. 风险 / 待定

- ServerProbe 子模块当前未初始化；实现前需要网络访问和独立仓库权限。
- 塔防插件仓库不在 JianManager 工作树，必须提供路径/权限才能完成真实适配提交；否则本 FR只能完成 SPI/CP 链路并标 partial。
- 伤害事件可能频率很高；ServerProbe 可按 actionRunId 在 250ms 内聚合 damage，但不得漏掉最终累计值。
- proxy cross_server 无 token 时按玩家名关联只作为兼容路径；塔防正式预设应把 token 传入业务插件。
- 不引入第三方 WS/队列依赖。
