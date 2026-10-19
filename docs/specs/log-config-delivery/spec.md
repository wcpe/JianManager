# 功能规格：日志采集配置下发链路（FR-503）

> 状态：**已实现**（代码 + 单测 + 前端已完成；真机端到端验收待做，见 §5 与 §7）
> 关联 PRD：**FR-503**　·　依赖：FR-502（采集规则引擎）
> 关联：FR-501（日志成本治理，其配置面同样受益于本通道）
> 动因：2026-10-15 需求——希望经面板配置采集规则与开关，且**每个 Worker 可单独配置**，未配置时走平台默认

本规格不得重新定义 FR-473 Shared Contracts；字段、状态与覆盖语义以
`docs/specs/worker-log-platform-contract/spec.md` 为准。

---

## 1. 背景与目标

**问题**：日志采集的规则与开关**无法经面板配置**，只能改各节点的 `worker.yml` 并重启。

**关键现状**（2026-10-15 调查）：

- `platform_settings`（`internal/controlplane/service/settings.go:137`）的注释明确其定位是
  「在 YAML+env 基线（cfg）之上叠加 `platform_settings` 的 DB 覆盖层」——
  **它只作用于 CP 自身**，**不存在任何下发 Worker 的通道**。
  ⚠️ **但下一条推翻了「无通道」的初判**。
- **心跳是双向流**（`rpc Heartbeat(stream HeartbeatRequest) returns (stream HeartbeatResponse)`），
  下行能力**天然存在**，且已有**三个成熟的同类先例**可直接照搬：
  | 先例 | 下发内容 | 生效方式 |
  |---|---|---|
  | FR-275（ADR-061） | `ws_token_secret` | Worker 比对生效值不同才热应用 + 持久化；≤1 心跳周期自愈 |
  | FR-446 | `direct_probe_*_timeout_ms` | 存内存并填入采集配置，无需重启 |
  | **FR-459** | `health_*` 策略组 | 用 **`optional bool`** 表达「CP 是否下发」（区分「关闭」与「未配置」） |
  ⇒ **本 FR 是「复用既有模式」，不是「新建通道」**。FR-459 是最贴近的模板。
- `SettingsService` 已有**可写白名单**机制（`isWritableSettingKey`）+ 值语义校验 +
  「可即时生效项写库后立即应用」的既有分工（`settings.go:357`），本项沿用。
- 前端目前**没有节点详情页**（只有 `NodesPage.tsx` 列表页），也无任何 Worker 级设置；
  实例侧已有 `instances/:id` 详情页可作形态参照。

**目标**：建立 **`worker.yml` 基线 → `platform_settings` 平台默认 → Worker 级覆盖** 的两层配置语义，
经**心跳链路**下发，前端提供配置界面。

**非目标**（范围外）：
- 不做**独立推送 RPC**（改完立即生效）——已评估为高风险（需 Worker 侧采集运行时动态重建），
  本项采用心跳下发（≤30s 生效），**不引入新的 gRPC 方法**。
- 不做**实例级**配置（用户已确认为两层：平台默认 + Worker 覆盖）。
- 不把**全部** Worker 配置搬上面板——本项只覆盖**日志采集**相关键（其余键留待后续按需扩）。

---

## 2. 需求（要什么）

1. **两层合并语义**：`worker.yml` 为基线；`platform_settings` 存平台默认；Worker 级可覆盖。
   优先级：**Worker 覆盖 > 平台默认 > worker.yml 基线**。
2. **心跳下发**：CP 在注册响应/心跳响应中携带该 Worker 生效的日志采集配置；
   Worker 应用变更，改完 **≤30s（一个心跳周期）生效**。
3. **新节点自动继承**：新 Worker 注册时无自身覆盖 ⇒ 自动使用平台默认。
4. **持久化**：Worker 侧配置持久化，重启后不丢（且离线可保持上次生效值）。
5. **前端**：
   - **新建节点详情页 `/nodes/:id`**，含「日志采集」页签（Worker 级覆盖）
   - 设置页新增「日志采集」分区（平台默认）
   - 按新功能需要整理设置页现有分区结构

---

## 3. 设计（怎么做）

### 3.1 配置键设计（`platform_settings` 新增）

**决策（2026-10-15 已确认）**：**复用 `platform_settings` 表**，用键前缀约定表达作用域——不新建表、不做迁移。

| 键 | 作用域 | 说明 | 默认 |
|---|---|---|---|
| `log.acq.rules_enabled` | 平台 / Worker | 是否启用规则引擎（FR-502） | `true` |
| `log.acq.realtime_tail` | 平台 / Worker | 实时采集开关（FR-504） | `true` |
| `log.acq.gz_import_enabled` | 平台 / Worker | gz 导入开关（FR-504） | `false` |
| `log.acq.gz_scan_interval` | 平台 / Worker | gz 定时扫描周期（FR-504） | 空（关闭） |
| `log.acq.path_overrides` | 平台 / Worker | 实例路径覆盖映射（JSON）；值须为**相对实例工作目录**的路径，绝对路径与折叠 `..` 后的逃逸一律拒绝（2026-10-19 裁定） | `{}` |

**作用域表达**：

```
平台级：log.acq.<key>
Worker 级：log.acq.<key>@node:<nodeUUID>
```

解析顺序（高 → 低）：`Worker 级命中` > `平台级命中` > `worker.yml 基线`。

> **为什么用键前缀而非建表**：`platform_settings` 已有完备的读写服务（`SettingsService` 的白名单、
> 值语义校验、"可即时生效项写库后立即应用"分工）与既有 API（设置页已在用）。复用后本项只需
> **扩白名单 + 加一层节点维度解析**，无需建表、迁移与新的读写服务——符合最小改动原则。
>
> **代价（如实登记）**：键名带 `@node:<uuid>` 后缀略绕；且节点 UUID 作为键的一部分，
> 节点删除后需清理（见 §6 风险）。

### 3.1.1 顺带纳入的既有下发项（已确认扩围）

本次一并将「**已在下发但面板不可配**」的项纳入面板（同一套机制，边际成本低）：

| 键前缀 | 现状 | 来源 |
|---|---|---|
| `health.*` | 已下发（心跳字段 9–17）但面板不可配 | FR-459 |
| `direct_probe.*` | 已下发（心跳字段 7–8）但面板不可配 | FR-446 |

> **共同点**：三组都走「心跳下发 + Worker 比对生效值热应用」，因此**同一套合并/下发/持久化代码可复用**。
> 这是把它们一并纳入的理由（而非顺手做无关功能）。

### 3.2 下发通道（复用心跳，不新增 RPC）

**链路**：Worker 心跳（现 30s 间隔）→ CP 计算该 Worker 生效配置 → 响应携带配置快照 → Worker 比对并应用。

**为什么可行**：心跳链路已承载 WS 令牌密钥下发（FR-275/ADR-061 已实现同类模式），
本项复用同一模式，**不引入新的传输机制**。

**配置版本号**：~~CP 侧维护单调递增 `configVersion`；Worker 上报当前版本，
CP 仅在版本不同时下发（避免每拍全量传输）~~。

> **实现定案（2026-10-15）**：**未引入版本号**，沿用 FR-459 的既有形态——CP 每拍**全量重发**生效值，
> Worker 侧比对生效值变化才应用（`ingest.AcqPolicyEqual`）。理由：心跳本就要发，5 个字段的传输
> 代价可忽略；而版本号需要 CP 维护状态 + Worker 上报，会引入「版本不同步导致不下发」这一新的
> 失败模式，收益（省几个字节）远小于风险。这与 §1 表中三个先例（FR-275/FR-446/FR-459）完全同型。

**Worker 侧应用**（关键约束）：
- 配置变更**不触发**采集运行时全量重建——只更新**下次采集轮读取的参数**
- 路径类变更（如规则、覆盖）**只对新登记或重登记的实例生效**，已在采实例保持原状
  （避免运行中切换路径导致账本断裂）
- 失败只告警不阻断心跳（配置问题不应导致节点掉线）

### 3.3 前端设计

**A. 新建节点详情页 `/nodes/:id`**

- 现有 `NodesPage.tsx` 是列表页（用 Dialog 做增删）；点节点行 → 跳转 `/nodes/:id`
- 页签结构（首版）：
  | 页签 | 内容 |
  |---|---|
  | 概览 | 节点指标、心跳、版本（复用既有组件） |
  | 实例 | 该节点上的实例列表（筛选） |
  | **日志采集** | **本项新增**——Worker 级覆盖配置 |
  | 日志 | 该节点的日志查询入口（可后置） |

- 「日志采集」页签内容：显示**平台默认值**与**本 Worker 生效值**两列对照，
  可逐项覆盖或恢复默认（「恢复默认」= 删除覆盖键）。

> **实现定案（2026-10-15）**：**未新建 `/nodes/:id` 路由**——调查发现 `NodesPage.tsx` **已不是**纯列表页，
> 而是「左列表 + 右详情」双栏；右栏 `NodeDetailPane` 已有 9 个分段（概览/实例/运行时/缓存/端口/代理/探针/监控/坏节点修复），
> 分段经 `?tab=` 可寻址（FR-128）。故改为**在同一面板新增「日志采集」分段**（`DETAIL_TABS` 加一项 + 一个渲染分支 +
> 独立组件 `NodeLogAcqPanel`）：既满足「节点详情承载 Worker 级配置」的目标，又不破坏既有布局与导航——
> 这正是 §6 风险条目「节点详情页是新页面……避免破坏现有布局」要求守住的边界。

**B. 设置页新增「日志采集」分区**

- 位置：现有 `SettingsPage.tsx` 新增分区（与诊断、出站测试等并列）
- 内容：平台默认的规则开关、gz 导入、实时采集、扫描周期、路径覆盖
- **顺手整理设置页**：仅按新功能需要调整分区结构（如分区标题命名统一、顺序归类），
  **不动与日志无关的现有内容**

**C. API**（前端调用）

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/api/v1/settings/log-acquisition` | 读平台默认 |
| PUT | `/api/v1/settings/log-acquisition` | 改平台默认 |
| GET | `/api/v1/nodes/:id/settings/log-acquisition` | 读该 Worker 生效值 + 覆盖值 |
| PUT | `/api/v1/nodes/:id/settings/log-acquisition` | 改 Worker 级覆盖 |
| DELETE | `/api/v1/nodes/:id/settings/log-acquisition` | 清除覆盖（恢复平台默认） |

权限：沿用既有 RBAC（平台管理员可改；Worker 级若后续需组管理员，按既有权限树扩）。

> **实现定案（2026-10-15）**：五个端点均已实现（`internal/controlplane/router/log_acq_settings.go`）。
> 其中**平台端点的前端未直接消费**——设置页继续走既有 `GET/PUT /settings`（`log.acq.*` 已入白名单，
> 只需修正分类映射即可零成本复用「草稿比对 / 未保存拦截 / 生效时机标注」等既有 UX）；新平台端点
> 保留给脚本与 Agent 集成（契约与节点端点同构）。节点端点由节点详情「日志采集」分段消费。

### 3.4 持久化

Worker 侧将生效配置持久化到数据根（如 `<dataRoot>/etc/log-acquisition.json`），
重启后先加载本地快照再等 CP 下发——**离线也能保持上次生效值**（与 FR-474 的
`ingest.state.json` 恢复模式一致）。

---

## 4. 任务拆分

- [x] 定义配置键与作用域表达（§3.1），确定复用 `platform_settings` 还是独立表 → **复用**，键 `log.acq.*` 与 `log.acq.*@node:<uuid>`
- [x] CP 侧：读取三层配置并合并，计算某 Worker 的生效配置（`SettingsService.LogAcqPolicy(nodeUUID)`）
- [x] 扩展心跳响应携带配置快照 ~~+ `configVersion`~~（见 §3.2 实现定案：按拍全量重发、Worker 侧比对）
- [x] Worker 侧：接收、合并、持久化、按 §3.2 约束应用（`heartbeat.logAcqApplier` + `ingest.AcqPolicy`）
- [x] API：5 个端点（§3.3C）+ 权限接线
- [x] 前端：节点详情「日志采集」分段（原计划新建独立页，改复用既有详情面板——见 §3.3A 实现定案）
- [x] 前端：设置页新增「日志采集」分区 + 按需整理分类映射
- [x] 测试：合并优先级、心跳生效、持久化、离线保持（单测；真机项见 §5）
- [x] 节点删除后的 Worker 级键清理（`PurgeArchived` 彻底清理路径顺带删除该节点全部 `log.acq.*@node:<uuid>` 键）

---

## 5. 验收标准

> 状态说明（2026-10-15）：`[x]` 项均已由**自动化测试**或**真机验收**覆盖（分别标注）；唯一未完成项为
> **浏览器人工验收**（自动化已全绿）。

- [x] **面板改配置 → 一个心跳周期内（≤30s）Worker 生效**（真机过：20:03:17 经 API 改平台默认 → **20:03:18.379** Worker 应用，实测 **1.4s**；改回 true 时同样在下一拍生效）
- [x] **Worker 级覆盖优先于平台默认**（真机过：平台 `gz_import_enabled=false`，节点级覆盖 `true` → 20:08:24 Worker 收到 `gzImportEnabled=true`；单测 `TestLogAcqPolicyWorkerOverrideWins` + `TestLogAcqNodeOverrideLifecycle`）
- [x] **恢复默认生效**（真机过：`DELETE /nodes/1/settings/log-acquisition` 后视图 `override` 为空、生效值回退平台默认，20:09:54 Worker 回退为 `gzImportEnabled=false`）
- [x] **新 Worker 注册自动继承平台默认**（单测：`TestLogAcqPolicyDefaults`、`TestLogAcqPolicyIsolatedBetweenWorkers` 的「未配置节点继承默认」）
- [x] **配置重启不丢**（真机过：重启 Worker 后日志出现「已恢复本地日志采集快照」，快照文件与重启前**逐字一致**；单测 `TestAcqPolicyPersistenceRoundTrip`）
- [x] **离线保持**（真机过：停 CP 后向 `proxy.log.0` 追加标记行 → **VL 可查到该行**（采集未因 CP 不可达而停），快照文件仍在；单测 `TestLoadAcqPolicyMissingFileIsNotAnError`/`CorruptFileReturnsErrorAndFallback`）
- [x] 配置变更**不导致**已在采实例的路径切换（单测 `TestRegisterInstancePolicyChangeDoesNotMoveExistingSource`）
- [ ] ⏳ 前端：节点详情分段可达、覆盖可增删、恢复默认生效（自动化已过：`tsc -b` + vitest 全绿（319 文件 / 2505 用例）+ i18n 键完整性；**浏览器人工验收未做**）
- [x] 测试全绿（`service` 8 例、`heartbeat` 6 例、`ingest` 12 例、`router` 5 例；四包全量回归绿）
- [x] **真机环境副作用为零**：两次 Worker 重启（二进制替换前后）**12 台游戏服 PID 零漂移**（`KillMode=process` 保护有效）；真机验证用的平台/节点覆盖已清理并恢复默认

---

## 6. 风险 / 待定

- ~~配置键作用域表达方式待定~~ → **已决策（2026-10-15）**：复用 `platform_settings` 键前缀，见 §3.1。
- **心跳下发的延迟可接受性**：用户已确认可接受 ≤30s；若后续需要即时生效，需另立 FR 做推送 RPC + 采集运行时热重建（高风险）。
- **设置页整理范围易失控**：已限定「仅按新功能需要调整」，评审时需守住这条。
- ~~**节点详情页是新页面**~~ → **已按实现定案处理（2026-10-15）**：`NodesPage` 已是「列表 + 详情」双栏且详情有分段机制，改为新增分段（见 §3.3A 实现定案），不新建页面与路由，既有布局零破坏。
- **键前缀方案的代价**（§3.1 已登记）：`@node:<uuid>` 后缀使键名略绕；**节点删除后需清理其 Worker 级键** → **已实现（2026-10-15）**：`NodeService.Purge`（彻底清理）在同一事务内删除该节点全部 `@node:<uuid>` 键（`deleteNodeScopedSettings`，按后缀匹配、不误伤其它节点与平台级键）；**软删除/归档不清理**——归档节点可恢复，保留其配置是有价值的。回归：`TestNodeService_Purge_RemovesNodeScopedLogAcqSettings`（含变异验证）。
- **扩围的边界**（§3.1.1）：`health.*` / `direct_probe.*` 虽已在下发，但其**字段语义与校验规则各自不同**（如 `health.probe_kind` 是枚举、超时是 duration）——纳入面板时须逐项接上各自的 `SettingKey*` 常量与校验分支，**不能只做通用的字符串透传**。（已按此实现：两类键的白名单与校验分支均逐项在位。）
- ~~**规则升级重建的两处缺口（A3/A6）**~~ → **已修（2026-10-16）**：重建前用只读探针检查「已读未投递残留」（按**投递水位**判定），有残留即**拒绝重建**并把排空交办给既有兜底环（非阻塞——刻意不在持 `registerMu` 时内联投递，避免 2026-10-03「登记被长活拖住」的同型形态）；重建失败时按快照恢复旧绑定与旧源（含账本镜像），恢复失败显式告警且刻意不恢复绑定以便下次登记自愈。连同「索引 `source_aux.config` 快照滞后」一起，三条都在 §7 末尾如实记录。

---

## 7. 实现记录（2026-10-15）

**已交付**（代码 + 单测 + 前端；真机待验）：

| 层 | 文件 | 内容 |
|---|---|---|
| 协议 | `proto/worker.proto` | `HeartbeatResponse` 字段 18–23（`log_acq_present` 为 `optional bool`） |
| CP·合并 | `internal/controlplane/service/log_acq_policy.go` | `LogAcqPolicy(nodeUUID)` 两层合并、`NodeScopedSettingKey`、设置视图、`DeleteOverrides`、节点清理 helper |
| CP·白名单 | `internal/controlplane/service/settings.go` | 5 个 `log.acq.*` 键 + `nodeScopeSplit`/`supportsNodeScope`（节点级覆盖仅对心跳下发类键开放）+ 逐项语义校验 + 展示项 |
| CP·下发 | `internal/controlplane/grpc/handler.go`、`apps/control-plane/main.go` | `SetLogAcqPolicyResolver` + 心跳响应注入 |
| CP·清理 | `internal/controlplane/service/node.go` | `Purge` 事务内删除该节点全部 `@node:` 键 |
| Worker·应用 | `internal/worker/heartbeat/log_config.go`、`heartbeat.go` | `logAcqApplier`（存在标记 / 去重 / 失败重试）+ `SetLogAcqPolicyApplier` |
| Worker·策略 | `internal/worker/logs/ingest/policy.go` | `AcqPolicy` / `SetAcqPolicy` / `AcqPolicySnapshot` / 持久化往返 |
| Worker·消费 | `internal/worker/logs/ingest/instances.go` | 登记时按策略收敛（规则开关回退、路径覆盖优先于 CP 下发与规则默认） |
| Worker·装配 | `apps/worker/main.go` | 启动加载本地快照 → 注入 applier |
| API | `internal/controlplane/router/log_acq_settings.go`、`router.go` | 5 个端点 + 权限接线 + 审计 |
| 前端 | `api/nodes.ts`、`components/NodeLogAcqPanel.tsx`、`pages/NodesPage.tsx`、`pages/settings-form.ts`、`pages/SettingsPage.tsx`、`i18n/{zh,en}.json` | 节点详情分段 + 设置页独立分区 + 分类映射 + 文案 |

**自动化证据**：

- `service` 侧 8 例（合并 / 白名单 / 视图 / 清理）、`heartbeat` 侧 6 例、`ingest` 侧 12 例、`router` 侧 5 例
- 变异验证 **7 条**（确定性转红后逐字还原）：规则开关回退、路径覆盖优先、存在标记 nil、失败重试、节点覆盖生效、写门禁、清理不误伤
- 前端：`tsc -b` 与 `vitest run` 全绿（含 i18n 键完整性）

**真机验收记录（2026-10-15，生产环境 `~/jm/prod`）**：

| 时刻 | 动作 | 观测 |
|---|---|---|
| 20:01 | 部署新 Worker 二进制（当时 CP 仍是旧版）并重启 | 12 服 PID **零漂移**；无下发字段 ⇒ Worker 保持本地默认（**向后兼容验证**） |
| 20:02 | 部署新 CP 二进制并重启 | Worker 重连正常，面板可用 |
| 20:03:17 → 20:03:18.379 | 经 API 改平台默认 `realtime_tail=false` | Worker 打「日志采集配置已据 CP 下发更新」并写快照，**实测 1.4s 生效** |
| 20:04:22 | 重启 Worker | 「已恢复本地日志采集快照」；快照与重启前**逐字一致**；12 服 PID 零漂移 |
| 20:06:11 → 20:06:47 | 停 CP，向 `proxy.log.0` 追加标记行 | **CP 不可达期间标记行仍进入 VL**（采集未停）；快照仍在 |
| 20:07:54 | 改回 `realtime_tail=true` | 下一拍生效（**去重语义正确**：值变化才应用） |
| 20:08:24 | 设节点级覆盖 `gz_import_enabled=true`（平台默认 false） | Worker 收到 `gzImportEnabled=true` ⇒ **Worker 覆盖优先** |
| 20:09:54 | 清除节点级覆盖 | Worker 回退 `false` ⇒ **恢复默认生效** |

> 真机验证用的平台/节点覆盖已清理并恢复默认；生产二进制替换前的旧版本均以 `.bak-<时间戳>` 保留。
> 另：首次下发时 Worker **未**产生任何痕迹——因为下发的值与 `DefaultAcqPolicy()` 相同而被去重跳过，
> 这是**预期行为**（值没变不该写盘），也说明「看不到日志」不等于「链路没通」。

**未完成（如实登记）**：

1. **浏览器人工验收**：节点详情「日志采集」分段与设置页「日志采集」分区的实际渲染与交互（自动化已全绿）。
2. `health.*` / `direct_probe.*` 的节点级覆盖只经白名单与合并单测，未做真机双命中。
3. `realtime_tail` / `gz_import_enabled` / `gz_scan_interval` 的**采集行为**在 FR-504 中实现（本项只保证可配 / 可下发 / 可持久化 / 可读取）。

**已闭环的三条（2026-10-16 补修；此前为「只登记未修 / 待观察」）**：

- **A3 规则升级重建前的「已读未投递」**：现在 `detachSourcesForRuleChange` **之前**用只读探针
  `instanceUndeliveredResidue` 检查残留——按**投递水位**（而非回收水位）比对 `pipe.WAL().Snapshot()`
  与账本 `Positions().Delivery`，统计 `Record.End > Delivery` 的条数与字节。有残留即**拒绝重建**
  并将排空交办给既有兜底环（`schedulePendingReplay`，O(1) 非阻塞）——刻意**不**在持 `registerMu`
  时内联投递（那正是 2026-10-03「登记被长活拖住」的同型形态，且会撞持久化门）。拒绝不是死结：
  兜底环本就会对 `Delivery < Read` 的源驱动同一入口；回归
  `TestRuleChangeRefusedWhileUndeliveredWALRemains` 同时锁定「排空后同一次升级被接受」。
- **A6 重建失败的补偿**：detach 前定格「旧绑定 + 旧 `SourceConfigs` + 旧账目镜像」快照；
  新源注册或落库失败时按快照恢复旧绑定与旧源。旧源恢复失败则显式 `slog.Error` 且**刻意不恢复
  绑定**——绑定在位会让 CP 下次同名登记走「绑定未变即幂等返回」的快速路径而永不建源，留空才能自愈。
  边界如实写在函数注释：恢复的是「源登记 + 绑定 + detach 前账目镜像」，镜像水位可能落后 ⇒
  可能重读一小段并重复投递（FR-497 允许重复），但不会整份重读、也不丢账目。回归
  `TestRuleChangeFailureRestoresOldSourceAndBinding`。
- **索引 `source_aux.config` 快照滞后**：根因是**触发面**（持久化只看采集账目变更，安静源永不声明
  变更），不是落库路径（根行每轮全量比对，一旦持久化周期跑起来就会写出新值）。修法：启动重算
  确实改写配置时置脏标记并 `requestPersist()`（非阻塞交办，不阻塞 `New`、不新增锁内比对）。
  回归 `TestStartupRecalcPersistsSourceConfigToIndex`。

三条分别做过变异验证（回退修复→断言转红→逐字还原）。
