# 实例能力画像抽象（FR-445）

> 状态：📋 计划　·　关联 PRD：FR-445　·　依赖：无　·　关联 ADR：[ADR-091](../../adr/091-instance-capability-profile.md)（实例能力画像与多形态详情界面）

## 1. 背景与目标

实例模型是**平坦两轴**：`type`（`minecraft_java` / `generic`，`internal/controlplane/model/instance.go:24-30`）× `role`（`backend` / `proxy` / `universal` / `beacon`，同文件 `:40-53`）。但详情界面只有**单一 MC 形态**：

- `InstanceConsolePage` 写死 9 个 Tab：`TAB_KEYS = ['overview','terminal','resource','plugins','metrics','players','business','bot','backup']`（`apps/control-plane-web/src/components/console/InstanceConsolePage.tsx:41`），内容深度绑死 Bukkit（TPS / 世界区块 / 插件 / 玩家 / 业务背包）。
- 适配靠零散 `if`：`MetricsSegment.tsx:38` 的 `if (inst?.role === 'proxy') return null`（代理不渲染探针卡）；`InstancesPage.tsx:1443/1450/1457` 的 `RoleBadge`、`:1629/1632` 的下拉项也各写一份角色分支。
- 线上 64 实例 = 1 `proxy`（`bc`）+ 1 `beacon`（Go 二进制，却被建成 `type=minecraft_java`，`startCommand=./beacon-1.1.0-linux-amd64`、`jdkId=0`）+ 62 `backend`。非 MC 实例打开详情页会看到一整片 MC 概念的空壳。

**目标**：引入**声明式**「实例能力画像」，以 `(type, role)` 为键声明该实例有哪些能力、是否 MC 语义、各能力的数据来源偏好；前端据画像显隐，不再硬编码角色分支。

**范围外（不做）**：不做用户自定义 Tab（插件化 UI 另议）；画像**不入 DB**；不改既有 `backend` 实例行为（其画像 = 现有 9 Tab 的子集）。

## 2. 设计

### 2.1 画像数据结构（Go，新增 `internal/controlplane/service/capability_profile.go`）

```go
type Capability string
const (
    CapOverview   Capability = "overview"
    CapTerminal   Capability = "terminal"
    CapFiles      Capability = "files"      // 文件配置（含 env 分段，FR-344）
    CapPlugins    Capability = "plugins"
    CapMetrics    Capability = "metrics"    // MC/服务端指标（TPS 等）
    CapPlayers    Capability = "players"
    CapBusiness   Capability = "business"
    CapBot        Capability = "bot"
    CapBackup     Capability = "backup"
    CapBCTopology Capability = "bcTopology" // BC 子服拓扑（FR-449）
    CapProcess    Capability = "process"    // 通用进程指标（CPU/内存/线程/句柄，FR-450）
    CapHealth     Capability = "health"     // 端口 + 健康检查（FR-450）
    CapConfig     Capability = "config"     // 结构化配置编辑（FR-451）
)

// 能力的数据来源偏好，按序降级（FR-447 编排语义，本 FR 只声明不实现编排）。
type DataSource string
const (
    SourceProbe  DataSource = "probe"
    SourceDirect DataSource = "direct" // MC 直探 SLP/Query（FR-446）
    SourceNode   DataSource = "node"
    SourceNone   DataSource = "none"
)

type InstanceCapabilityProfile struct {
    Type         string                 `json:"type"`         // 实现形态
    Role         string                 `json:"role"`         // 拓扑职责
    MCSemantics  bool                   `json:"mcSemantics"`  // 是否具备 MC 服务端「世界语义」（world/chunk/TPS）；只影响 Tab 内字段取舍，不作 Tab 级门控
    Capabilities []Capability           `json:"capabilities"` // 有序，决定 Tab 顺序
    Sources      map[Capability][]DataSource `json:"sources,omitempty"`
}
```

`type` 是**实现形态**、`role` 是**拓扑职责**，两轴正交（ADR-091 §理由）：beacon 既是「原生二进制」又是「配套服务」，故键必须是 `(type, role)` 组合而非单轴。

### 2.2 注册表（代码内描述符，不入 DB）

```go
var capabilityRegistry = map[profileKey]InstanceCapabilityProfile{ ... }

func ProfileFor(t model.InstanceType, r model.InstanceRole) InstanceCapabilityProfile {
    if p, ok := capabilityRegistry[profileKey{t, r}]; ok { return p }
    return universalFallback // 未知组合回退安全子集：overview/terminal/files + backup
}
```

**为什么代码内注册表而非 DB**：画像每一项都对应一段前端渲染 + 后端数据源实现，二者随代码发布；存 DB 会让「声明了某 Tab 但代码还没有」成为可能状态（ADR-091 §理由）。新增产品 = 加一条描述符。

### 2.3 内置画像

| (type, role) | MCSemantics | Capabilities（有序） | 说明 |
|---|---|---|---|
| `(minecraft_java, backend)` | true | overview, terminal, files, plugins, metrics, players, business, bot, backup | = 现有 9 Tab，行为不变 |
| `(minecraft_java, proxy)` | false | overview, terminal, files, bcTopology, players, plugins, process, health, config, backup | BC 代理；无世界/TPS，故**无 `metrics`(TPS) 档**，自身运行指标走 `process`；有跨服玩家（`players`）与 BungeeCord 插件（`plugins`） |
| `(generic, beacon)` | false | overview, terminal, files, process, health, config, backup | 配套服务（FR-450） |
| `(generic, universal)` | false | overview, terminal, files, process, health, config, backup | 通用二进制（FR-450） |
| 兜底 universal | false | overview, terminal, files, backup | 未知组合不白屏 |

> **`mcSemantics` 语义（重要，避免与 FR-448 冲突）**：它**不是 Tab 级门控**，语义收窄为「**具备 MC 服务端世界语义**（世界 / 区块 / TPS）」。Tab 显隐**只由 `capabilities` 决定**；`mcSemantics` 仅决定**同一 Tab 内的字段取舍**（如 `overview` 对 backend 显示世界/TPS，对 proxy 显示连接与跨服玩家）。
>
> **`players` 的按角色语义**（同一能力、不同含义）：`backend` 的 `players` = **单服实名玩家**（探针/Query 直取）；`proxy` 的 `players` = **跨服分布**（谁在哪个子服，聚合各后端，见 FR-449）。两者共用 `players` 能力，但前端按角色渲染不同视图。

> `beacon` 画像依赖 FR-454 归正其 `type` 为 `generic`（当前被误建为 `minecraft_java`）；归正前 `(minecraft_java, beacon)` 走兜底画像，不展示 MC Tab。

### 2.4 后端暴露画像

- `GET /instances/:id` 响应增 `capabilities`（画像结构体，见 2.1）；`agent_get_instance`（MCP）同源透出。
- 画像**按当前 `type`/`role` 现算**，不落库、不缓存到实例行。
- 修改面：`internal/controlplane/service/instance.go` 的详情组装点 + `internal/controlplane/router/instance.go` 的响应映射。

### 2.5 前端门控

- 新增 `apps/control-plane-web/src/lib/capabilities.ts` + `useInstanceCapabilities(inst?)` hook：优先读后端下发的 `capabilities`，缺失时按 `(type, role)` 本地兜底（离线/mock 兼容）。
- `InstanceConsolePage.tsx`：`TAB_KEYS`/`TAB_GROUP_BREAK`/`TAB_LABEL_KEY`/`TAB_ICON` 保留为全量登记表，**渲染循环（`:438`）按 `useInstanceCapabilities` 过滤**后输出（顺序仍取画像 `capabilities`）。`mountedTabs`（`:472`）仅纳入可见 Tab；若当前 `?tab=` 落在隐藏 Tab 上，回退 `overview`。
- 删除散布的硬编码：`MetricsSegment.tsx:38`、`InstancesPage.tsx` 的角色分支改查画像。`InstanceInfo`（`api/instances.ts:12`）增可选 `capabilities` 字段。

## 3. 任务拆分

1. 后端：新增 `capability_profile.go`（结构体 + 注册表 + `ProfileFor` + 兜底），附单测覆盖四种画像与未知组合。
2. 后端：`GET /instances/:id` 与 `agent_get_instance` 输出 `capabilities`。
3. 前端：新增 `lib/capabilities.ts`（本地兜底表，与后端注册表一一对应）+ `useInstanceCapabilities`。
4. 前端：`InstanceConsolePage` 改为按画像过滤 Tab（含深链回退），删除 `MetricsSegment` 角色分支。
5. 前端：`api/instances.ts` 增 `capabilities?` 类型；`InstancesPage` 角色分支改查画像。
6. devmock：`packages/devmock` 实例详情响应补 `capabilities`。

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | `(minecraft_java, backend)` 详情页仍是原 9 Tab，行为零变化 | 单测 + DOM 测试 |
| 2 | `(minecraft_java, proxy)` 不渲染探针卡与业务/世界 Tab | DOM 测试 |
| 3 | `(generic, beacon)` / `(generic, universal)` 无任何 MC 专有 Tab | DOM 测试 |
| 4 | 深链 `?tab=plugins` 打到非 MC 实例时回退 `overview` 不白屏 | DOM 测试 |
| 5 | 未知 `(type, role)` 回退安全子集 | 单测 |
| 6 | 前端无新增 `role === '...'` 硬编码（画像外） | 代码审查 |
| 7 | **真机**：线上 `bc`（proxy）与 `beacon` 打开详情页，Tab 集合与后端画像一致 | 真机过 |

## 5. 风险 / 待定

- **beacon 的 `type` 归正（FR-454）**：归正前 `(minecraft_java, beacon)` 走兜底画像；需确认归正影响的其它消费方（列表筛选、探针分配）。
- **画像与前端的双份登记表**：后端注册表与前端 `lib/capabilities.ts` 兜底需保持同步，考虑加一条一致性单测（枚举比对）。
- **`capabilities` 与既有 `role` 消费点的收敛边界**：列表/拓扑的角色徽标（FR-136）本 FR 不强制迁移，留待 FR-452/453 一并处理。
