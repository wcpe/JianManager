# 功能规格：JBIS 业务对接 — 背包域（FR-124~127, M3）

> 状态：开发中　·　关联 PRD：FR-124~127　·　关联 ADR：ADR-026/028 + ServerProbe ADR-0016/0017 + AllinInventorySync ADR-0011/0012/0014
>
> 设计总纲见同目录 `design.md`；M1/M2（经济域 FR-115~123）已交付，本 spec 为 M3 背包整域在途详情。

## 1. 背景与目标

JBIS「一个 ServerProbe = 本服唯一全能 agent」：核心链路 CP/Worker/桥/DB/UI **插件无关**（只认 `domain + action + payload信封 + dedupKey`），唯一认识具体插件的是探针侧 per-plugin 适配器(Provider) + 每域 manifest。M3 背包整域 = 扩 AllinInventorySync api（读写门面）+ ServerProbe 背包适配器 + 汇聚存储 + 定制页。数据所有权不变：业务真源在插件存储，JM 侧存汇聚镜像 + 操作审计。

- **横切约束**：每条 FR 验收含 i18n + 暗/亮色 + 真机；高危写（改背包）必须二次确认 + 审计留痕贯通。

## 2. 需求与验收（逐 FR）

#### FR-124: 扩 AllinInventorySync api 导出读写门面
- **优先级**: P2 | **依赖**: 无（独立仓，api 侧已完成） | **关联 ADR**: AllinInventorySync 仓 ADR-0011 / ADR-0012
- **状态备注**: AllinInventorySync 仓 FR-21 api 扩展已实现+单测+落地，曾发 mavenLocal `allininventorysync-api:1.2.0`（Kotlin DTO）。**其后该仓发布 2.0.0：对外 api 收口、纯 Java + Lombok 化（AIS 仓 `803ae4f`），并把背包/末影箱写门面入参退回不透明分区字节（`byte[] base/edited`）——外部集成无法从结构化物品构造、读门面也不外泄分区字节，故结构化物品写不可外部消费**；探针侧据此把物品写暂降级（读 view / 基础属性写 / 事件保留，ServerProbe ADR-0017）。真 apply 经 NbtCodec 依赖 `net.minecraft.*` 移 E2E，随 FR-126/127 真机联调
- **描述**: 在 AllinInventorySync 仓扩 `api/` 模块，导出背包读写门面（原公开 api 零写入、读不到离线）。**跨仓 FR，在该仓走其自身 SDD 流程（其 FR-21 / ADR-0011+0012）**
- **验收**:
  - [x] 该仓写自身 ADR（ADR-0011 写门面+回执 / ADR-0012 结构化读+写安全）
  - [x] `getPlayerInventory(uuid)`：回源加载（含离线、纯读不 bump、不存在返 null）+ 结构化 ItemDto（material/amount/displayName/lore/enchants + nbtBase64 全保真）
  - [x] `writeInventory/writeEnderChest/writeBasicAttrs(base+edited delta)`：带 WriteResult 回执 + 持久业务幂等键（requestId 防重发刷物品）+ 在线归属校验（OWNED_ELSEWHERE 拒改他服在线）+ 委托 InventoryEditService.executeWrite delta 通道（两层锁 + CAS）。**注：2.0.0 起 `writeInventory/writeEnderChest` 入参为 `byte[]` 分区字节（不可外部消费，仅供其自身 GUI / 持有字节者）；`writeBasicAttrs` 仍收定形 `BasicAttrsDto`，外部可消费**
  - [x] ItemStack↔JSON codec（ItemStackCodec，Bukkit 序列化 base64 全保真，信封承载）
  - [~] **真机**：第三方经 api 读任意玩家背包 + 带回执发/收物品 + 重发幂等不刷物品 —— 幂等命中/归属拒绝短路由 core 单测覆盖；真 apply（NbtCodec 触 `net.minecraft.*`）经 E2E 真服验，待 FR-125 链路就绪联调

#### FR-125: 背包 Provider
- **优先级**: P2 | **依赖**: FR-117, FR-124 | **关联 ADR**: JM ADR-026 / ServerProbe ADR-0016
- **状态备注**: ServerProbe 子模块 platform-bukkit 已实现+单测+落地（初版 commit `f2b50c2`+`0f86ed4`）。**AllinInventorySync 2.0.0 发布后随其 api 重对接**——`compileOnly` 升 `2.0.0`、DTO 纯 Java 化（Kotlin 侧构造改位置参）、物品写 `writeInventory/writeEnderChest` 暂降级不进 manifest（其门面入参退回分区字节，ServerProbe ADR-0017），读 view / 基础属性写 / 追踪事件保留；`./gradlew build` 全绿。经整链下发读 + 属性写 + 追踪事件汇聚的真机随 FR-126/127 统一收口，同经济域
- **描述**: ServerProbe 背包适配器 `InventoryProvider` wrap AllinInventorySync 2.0.0 api（纯 Java），含读 view + 基础属性写 + 追踪事件订阅 + manifest（物品写暂不提供）；物品过桥契约见 ServerProbe ADR-0017（取代 ADR-0016 决策 1/2）
- **验收**:
  - [x] `inventory.view`（getPlayerInventory 回源含离线 → 结构化视图，玩家无数据回 exists=false）+ `inventory.writeBasicAttrs`（经写门面 getInventoryWriteApi，WriteResult 回执透传 success/online/newDataVersion/errorCode）+ 背包域 manifest（仅 view + writeBasicAttrs）
  - [x] 守写契约（writeBasicAttrs）：幂等键 taskId（CP 注入）→ 写门面 requestId 持久去重（缺则拒绝）、base+edited 净改动 delta 透传、operator 透传（空回退 JianManager）、future 有界阻塞取回执
  - [x] **物品写 writeInventory/writeEnderChest 暂不提供**：AllinInventorySync 2.0.0 物品写门面入参退回不透明分区字节，外部无法从结构化物品构造，dispatch 收到即明确降级（itemWriteUnsupported）、不进 manifest（ServerProbe ADR-0017，待其导出可外部消费的结构化物品写门面再恢复）
  - [x] 订阅 `TrackedItemActionEvent`（重点物品流转）→ emitBusinessEvent（domain=inventory，dedupKey=`playerUuid:action:occurredAtMs:seq`），软依赖 `@SubscribeEvent(bind=FQCN)` + OptionalEvent.get 避免漏注册
  - [x] 纯逻辑抽 `InventoryEnvelope`/`InventoryEventEnvelope`（单测 10+3 例全绿）；`compileOnly allininventorysync-api:2.0.0` + plugin softdepend AllinInventorySync
  - [ ] **真机**：inventory.view 看到真实物品清单、writeBasicAttrs 生效且幂等、追踪事件汇聚（随 FR-126/127 统一收口；物品写暂不在范围内）

#### FR-126: 背包汇聚与存储
- **优先级**: P2 | **依赖**: FR-116, FR-121 | **关联 ADR**: ADR-028
- **描述**: 背包追踪事件汇聚 + 操作审计 + 离线写"待生效"状态呈现
- **验收**:
  - [ ] 追踪事件（JOIN_CARRY/DROP/PICKUP/MOVE_TO_CONTAINER）汇聚去重落库
  - [ ] 背包操作审计表（谁对谁做了什么物品操作）
  - [ ] 离线写后端如实呈现"已写入、待玩家上线生效"，不谎报"已到手"

#### FR-127: 背包定制页
- **优先级**: P2 | **依赖**: FR-119, FR-121, FR-125, FR-126
- **描述**: 背包业务定制页（快照查看/物品清单/远程干预），高危操作二次确认。**注：远程发/收物品依赖 AllinInventorySync 物品写门面，当前 2.0.0 不可外部消费（探针侧暂降级，ServerProbe ADR-0017）；FR-127 落地时若物品写仍不可用，则限于背包/末影箱查看 + 基础属性干预，待物品写门面恢复再补物品远程干预**
- **验收**:
  - [ ] 玩家背包快照查看 + 物品清单展示（经导出的 ItemDTO）
  - [ ] 远程发物品/收物品走二次确认 UI；离线写显示待生效；i18n + 暗/亮色
  - [ ] **真机**：定制页看真玩家背包、远程发物品生效（在线即时/离线下次登录）
