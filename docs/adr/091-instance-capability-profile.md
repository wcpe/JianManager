# ADR-091: 实例能力画像与多形态详情界面

- **日期**: 2026-09-21
- **状态**: accepted
- **关联**: FR-445 / FR-448 / FR-449 / FR-450 / FR-452 / FR-453 · 增强 [ADR-090](090-binary-provision-and-beacon-sync.md)（通用二进制搭建与 Beacon 可选协同——引入 role/type 两轴，本 ADR 适配的动因）· [ADR-056](056-server-centered-console.md)（服务器为中心的统一控制台——被多形态适配的详情壳）· [ADR-033](033-instance-grouping-tree.md)（实例组织分组树——列表/拓扑的分组维度来源）

## 上下文

JianManager 的实例是**平坦模型**：`type`（`minecraft_java` / `generic`）与 `role`（`backend` / `proxy` / `beacon` / `universal`）两个轴，实例之间无层级、无父子。

但实例详情界面是**单一 MC 形态**：9 个 Tab（概览/终端/文件环境/插件/监控/玩家/业务/Bot/备份）的内容深度绑死 Bukkit 语义——TPS、世界/区块、插件、玩家、业务/背包/经济，全部假设"实例是一个 Minecraft 服务端"。而平台上已实际运行：

- `bc`（`role=proxy`）：BungeeCord 代理，无世界/TPS，但有子服拓扑与跨服玩家
- `beacon`（`role=beacon`，实际是 Go 二进制）：**被建成 `type=minecraft_java`**，卡片与详情页与普通 MC 服毫无区别（真机确认：`startCommand=./beacon-1.1.0-linux-amd64`、`jdkId=0`、`serverPort=0`）
- 通用二进制（`type=generic`）：模型里已存在该类型，但线上无实例使用，界面无对应呈现

现状的适配方式是零散的 `if` 分支（如 `MetricsSegment` 里 `if role === 'proxy' return null` 隐藏探针卡），既不可扩展也无统一声明处。用户明确要求：**按 role/type 适配界面、探针有无两套样式、二进制/beacon 有专属视图，且要"做好抽象，能适配更多产品"**。

## 决策

1. **引入「实例能力画像」（InstanceCapabilityProfile）**：以 `(type, role)` 为键，映射到一份**声明式**画像。画像声明该实例：
   - **有哪些 Tab / 能力**（如 `overview` / `terminal` / `files` / `metrics` / `players` / `worlds` / `plugins` / `bcTopology` / `process` / `health`）
   - **每个能力的数据来源偏好**（探针 / 直探 / 节点指标 / 无）
   - **是否 MC 语义**（决定是否显示世界/区块/玩家等概念）

2. **前端据画像显隐，不再硬编码角色分支**：所有 `if role === 'xxx'` 改为查画像。画像缺失时回退到 `universal` 的安全子集（概览/终端/文件/日志）。

3. **画像为代码内注册表**（descriptor registry），**不入 DB**：能力是**代码语义**（与实现强绑定），不是用户数据；新增产品只需注册一条描述符，无需迁移。

4. **画像同时作为列表/拓扑的组织维度来源**（FR-452/453）：列表树表与拓扑的"角色/类型"分组维度即读画像。

5. **归正 `beacon` 的 `type`**（FR-454 的一部分）：二进制实例应为 `generic`，`role=beacon` 是叠加在其上的角色。`type` 与 `role` 正交。

## 理由

- **为什么声明式而非 if-else**：产品数会增长（"还能适配更多产品"是明确诉求）。`N` 个产品 × `M` 个 Tab 的 if-else 矩阵会爆炸；声明式画像把"加一个产品"退化为"加一条描述符"。
- **为什么画像不入 DB**：画像的每一项都对应一段前端渲染 + 后端数据源实现，二者随代码发布；存 DB 会让"画像里声明了某个 Tab 但代码还没有"成为可能状态，制造不一致。
- **为什么 type 与 role 正交**：`type` 是**实现形态**（Java 进程 / 原生二进制），`role` 是**拓扑职责**（代理 / 子服 / 配套服务）。beacon 既是"原生二进制"（type）又是"配套服务"（role），两轴独立才能表达。
- **为什么提供 universal 回退**：未知/新类型不应白屏；安全子集（概览/终端/文件/日志）对任何进程都成立。

## 后果

- 新增 `model.InstanceCapabilityProfile` 与注册表（CP 侧 `service/capability_profile.go`）；实例 API（`GET /instances/:id`、`agent_get_instance`）返回 `capabilities`。
- 前端新增 `useInstanceCapabilities(instance)` 门控 hook；`InstanceConsolePage` 的 `TAB_KEYS` 改为按画像过滤；删除现有散布的 `role === 'proxy'` 分支。
- `MetricsSegment` / `ServerStateSegment` / `BusinessSegment` 等 MC 专有段加画像门控。
- 画像同时供 FR-452（列表维度）与 FR-453（拓扑）消费。
- FR-454 归正 `beacon` 实例的 `type` 为 `generic`（数据修复，需确认影响面）。
- 纯新增抽象，不破坏既有实例的行为（`backend` 画像 = 现有 9 Tab 的子集）。

## 备选

- **前端继续硬编码 if-else**：被否——不可扩展，正是当前痛点。
- **能力画像存 DB / 可用户配置**：被否——能力与代码实现强绑定，存 DB 制造"声明与实现不一致"的可能状态；确有"用户自定义 Tab"需求时应作为独立能力（插件化 UI）另议。
- **用 role 单轴表达（type 并入 role）**：被否——会让 `beacon-binary` / `beacon-java` 这类组合名爆炸，且违反两轴正交的事实。
- **为每种产品写独立详情页组件**：被否——重复实现概览/终端/文件等通用能力，与"复用"背道而驰。
