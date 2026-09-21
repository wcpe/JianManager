# 实例配置源明面化（FR-451）

> 状态：📋 计划　·　关联 PRD：FR-451　·　依赖：FR-233（实例配置编辑）、FR-031（配置版本化）、FR-445/ADR-091（能力画像决定配置项适用性）　·　关联 ADR：ADR-092（配置源明面化与 MC 直探）、ADR-044/016（RCON 退役）

## 1. 背景与目标

**现状痛点**：实例配置入口分散且深藏。启动命令 / JVM 参数走 `instance.StartCommand`（`internal/controlplane/model/instance.go:87`）与派生用的 `LaunchSpec`（`:90`），只在 `EditInstanceConfigDialog.tsx`（FR-233）这样一个偏门弹窗里改；`server.properties` 只能经文件管理器（FR-008）或结构化字段补丁（FR-031 的 `POST /instances/:id/configs/write-fields`，入口更深）。运维反馈：**"不要放到犄角旮旯里，放出来配置，或者直接引用文件"**。

同时 FR-446/447 的 Query 档依赖 `enable-query` / `query.port` 被正确配置——这两项散在 `server.properties`，运维心智高，平台又无法保证与端口分配一致。

**目标**
- **配置项二态来源**：每个受管配置项显式声明来源——**内联值**（平台持有、可编辑、可版本化）或**文件引用**（外部文件提供，平台**不覆写**，只读展示生效值预览）。两态互斥、显式，杜绝"平台改了却被文件覆盖"的静默冲突。
- **明面化入口**：启动参数 + `server.properties` 关键项（含 `enable-query` / `query.port` / `view-distance`）在实例详情/配置区**直接可见可改**，不再要求进文件管理器。
- `enable-query` / `query.port` 纳入受管项，由平台经内联值注入，复用既有端口分配（`allocPortsForNode`）与端口唯一性校验（`schema/crosscheck.go`）。

**范围内**：受管配置项登记表 + 二态来源模型、读/写 API、详情页明面化 UI、Query 两项纳入受管、跨实例端口校验扩展到内联值。

**不做（范围外）**：不做通用"任意配置文件可视化编辑器"（复杂文件仍走 FR-008 文件管理器）；不做配置模板批量下发 / 漂移收敛（FR-458）；不改配置版本化内核（`ConfigService.Write/Versions/Diff/Rollback` 原样复用）。

## 2. 设计

### 2.1 受管配置项与二态来源（核心模型）

新增**受管配置项登记表** `model.InstanceConfigSource`：

```go
type ConfigSourceKind string // "inline" | "file"
const (
    ConfigSourceInline ConfigSourceKind = "inline" // 平台持有值，可写、可版本化
    ConfigSourceFile   ConfigSourceKind = "file"   // 外部文件提供，平台不覆写
)

type InstanceConfigSource struct {
    ID         uint
    InstanceID uint
    ItemKey    string // 受管项标识：见下表
    Source     ConfigSourceKind
    // Source=inline 时的值（启动项为命令串，properties 项为字符串值）
    InlineValue string
    // Source=file 时引用的文件路径（相对工作目录，如 "server.properties" / "custom/server.properties"）
    FilePath string
    // Source=file 时文件内对应的键（properties 平铺键），供读取生效值预览
    FileKey string
    UpdatedAt time.Time
}
```

`ItemKey` 是**稳定标识**，分三类：

| ItemKey 前缀 | 例 | 内联落点 | 文件引用语义 |
|---|---|---|---|
| `startup.` | `startup.command`（FR-233 `StartCommand`）、`startup.launchSpec` | 写 `Instance.StartCommand` / `LaunchSpec`（经 `InstanceService.Update`） | 引用一个含启动命令的文件（少用，主要为对称） |
| `props.` | `props.enable-query`、`props.query.port`、`props.view-distance`、`props.max-players`、`props.motd` | 写 `server.properties` 对应键（经 `ConfigService.WriteFields`） | 引用 `server.properties`（或另名文件）的该键 |
| `flags.`（预留） | 未来 JVM 参数 | — | — |

`props.` 项集合复用既有 schema 注册表 `schema.ServerPropertiesModel()`（`internal/controlplane/service/schema/schema.go:119`）——它已含 `enable-query`（`:137`）、`query.port`（`:138`）、`view-distance`（`:130`），明面化直接以该表的 `Group`/`Description`/`Choices` 渲染表单，**不另建真源**。

### 2.2 二态互斥与冲突防护

- **互斥**：一项同一时刻只能是 `inline` 或 `file`。切换来源时更新登记表并记录审计。
  - `inline → file`：平台停止持有该值，`InlineValue` 置空，记 `FilePath`/`FileKey`；**不删除文件中原值**（尊重用户"文件是真源"的选择）。
  - `file → inline`：以文件当前生效值为初值填入 `InlineValue`（用户可见的迁移基线），此后平台负责写。
- **冲突防护**：当某项为 `file` 时，直接经文件管理器 / `config_write_fields` 改到该键，平台**不静默覆盖**——写入前置校验命中受管 `file` 项则返回 warning（"该项已声明由文件引用，平台编辑器改动可能与生效值不一致"），与 `config.CrossCheck` 同一响应风格（`internal/controlplane/router/config.go:270`）。
- **生效值预览**：`file` 项只读展示——经 `ConfigService.Read`（`internal/controlplane/service/config.go:197`）解析该文件、用 `schema.MatchPath` + `ApplyTypes` 取该键值渲染，不进版本历史、不可编辑。

### 2.3 读写 API（扩既有 config 路由，不新建协议）

挂到既有 `/instances/:id/configs` 组（`internal/controlplane/router/config.go:208`）：

- `GET /instances/:id/configs/surface` → 受管项清单：每项 `{itemKey, source, inlineValue?, filePath?, effectiveValue?, effectiveSource, editable}`。`effectiveValue` 对 `file` 项是解析预览，对 `inline` 项即值本身。门禁 `file.read`/`instance.read`。
- `PUT /instances/:id/configs/surface` → 按项 `{itemKey, source, inlineValue?, filePath?, fileKey?}` 更新：
  - `source=inline` 且 `startup.*` → 调 `InstanceService.Update`（`instance.go:713`，`StartCommand *string`）。
  - `source=inline` 且 `props.*` → 调 `ConfigService.WriteFields`（`config.go:231`，保留注释/顺序，生成版本）。
  - `source=file` → 仅写登记表（+ 预览校验文件/键存在）。
  - 门禁 `file.write`/`instance.write`。

> **复用而非新建**：`startup.command` 的持久化与"对下次启动生效"语义完全复用 FR-233 的 `Update`↔`registerOnWorkerLocked` 链路（`instance.go:788`）；`props.*` 的持久化、版本、diff、回滚完全复用 FR-031 的 `WriteFields`/`Versions`/`Diff`/`Rollback`。本 FR 只新增**来源层**与**明面化入口**。

### 2.4 端口唯一性校验扩展

`schema.CheckPortConflicts`（`internal/controlplane/service/schema/crosscheck.go:38`）现从**文件内容**取 `server-port`/`query.port` 判重。明面化后 `inline` 项的端口不在文件里（或文件里被覆盖），须把登记表中的 `inline` 端口值**并入校验输入**，否则两个实例内联同端口会漏检。方案：在 `ConfigService.CheckCrossFile`（`config.go:363`）聚合同节点实例时，对每个实例用「登记表 inline 值优先、否则文件值」构造 `ParsedConfig` 再入 `CheckAll`。`allocPortsForNode`（`ports.go:82`）已为 MC 实例分配同节点唯一的 `server`/`query`/`probe` 端口，内联注入默认取该分配值，两者不冲突。

### 2.5 前端明面化

- 把 `EditInstanceConfigDialog.tsx`（FR-233）从"启动命令/JDK/自启"扩为**配置源面板**：每项一行，左侧来源切换（内联 / 引用文件），右侧按来源渲染可编辑输入或只读预览 + "打开文件"跳转（复用 `config-explorer/ConfigFileEditor.tsx`）。
- 详情页新增「关键配置」分区，直接嵌入 `props.*` 关键项（`enable-query`/`query.port`/`view-distance`/`max-players`/`motd`），用户不必进文件管理器。
- **按能力画像显隐**（FR-445/ADR-091）：`props.*` 与 `startup.*` 仅对 MC 语义实例（backend/proxy/`minecraft_java`）呈现；beacon/generic 走各自画像的配置项，前端不做 `if role==='proxy'` 硬编码。
- `server.properties` 改动**下次启动生效**（MC 只在启动读取），UI 明确提示"需重启生效"，与现有 FR-233 一致。

## 3. 任务拆分

1. **模型 + 迁移**（`model.InstanceConfigSource` + 迁移脚本/自动迁移；`ItemKey` 常量表）。**无依赖。**
2. **来源服务**（`service/config_source.go`）：登记表 CRUD、二态互斥与迁移语义、生效值预览（读 `ConfigService.Read`）、冲突防护钩子。**依赖 1。**
3. **API + 门禁**（`router/config.go` 加 `surface` 路由；`CrossCheck` 并入 inline 端口）。**依赖 2。**
4. **端口校验扩展**（`config.go:CheckCrossFile` 用登记表 inline 值构造输入；补 `crosscheck` 单测）。**依赖 2。**
5. **`enable-query`/`query.port` 纳入受管默认**：建服/`provision` 时以 `allocPortsForNode` 的 `QueryPort` 预置 `props.query.port` 内联项；与 FR-446 Query 档联动。**依赖 2/3，且与 FR-446 任务 3 协同。**
6. **前端配置源面板**（改写 `EditInstanceConfigDialog.tsx` + 详情页关键配置分区 + FR-445 画像显隐）。**依赖 3。**
7. **文档同步**：PRD 状态、API.md（config 路由新端点）、ARCHITECTURE（配置源模型）、CHANGELOG。

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 详情页可直接改启动参数（`startup.command`）并持久化，对下次启动生效 | 真机（要真机过） |
| 2 | 详情页可直接改 `server.properties` 关键项（含 `enable-query`/`query.port`/`view-distance`）并持久化、生成版本 | 真机（要真机过） |
| 3 | 可把某项切为**引用文件**模式；平台**不覆写**该文件；面板只读展示解析出的生效值 | 真机（要真机过） |
| 4 | 二态互斥：切 `file→inline` 以文件现值为初值；切 `inline→file` 不删文件原值 | 单元 + 真机 |
| 5 | 命中受管 `file` 项的编辑器改写被提示（不静默覆盖） | 单元 + 真机 |
| 6 | 两实例内联同 `query.port` / `server-port` 被 `cross-check` 检出 | 单元（`schema` 测试） |
| 7 | 未开 Query 的实例在内联设 `enable-query=true`+`query.port` 后重启，FR-446 Query 档可用 | 真机（跨 FR 联动） |
| 8 | beacon/generic 实例不显示 MC 专有配置项（按画像显隐） | 真机 + 前端断言 |
| 9 | 切换来源、内联写入均写审计 | 单元 + 真机 |

## 5. 风险 / 待定

- **"文件引用"的粒度**：`props.*` 引用指向**文件内某个键**还是**整个文件**？本规格取"整文件引用 + 键级预览"——引用 `server.properties` 后其全部受管键都以文件为准；若运维只想某几项走文件、其余走内联，需 **键级** source。**首版建议键级 source**（每 `props.<key>` 独立二态），更贴合"平台管一部分、文件管一部分"的混用诉求；此点需拍板。
- **`file → inline` 的写入位置**：若引用的是非默认路径文件（如 `custom/server.properties`），切回内联后平台写到哪里？需明确"内联项固定写标准路径 `server.properties`"，避免写出两个文件。
- **生效时机**：`server.properties` 与启动参数都是**下次启动生效**；若配合 FR-457 滚动重启可自动收敛，本 FR 仅提示、不自动重启。
- **beacon/binary 的探针误配**（FR-454）与本 FR 的画像显隐同源：beacon 不应出现 `server.properties` 项——依赖 FR-445 画像正确落地，否则会显示无效配置项。
- **旧实例兼容**：登记表为空的存量实例，`surface` 接口应把 `startup.command` 视作 `inline`（取 `Instance.StartCommand`）、关键 props 视作"未受管/文件隐含"，保证平滑过渡、不报错。
