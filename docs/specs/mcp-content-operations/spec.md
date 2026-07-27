# 功能规格：MCP 文件、配置与插件运维强类型工具

> 状态：草拟　·　关联 PRD：FR-397　·　优先级：P1　·　依赖：FR-395（已实现）　·　可与 FR-396/398 并行　·　关联 ADR：080　·　分支：feature/fr-397-mcp-content-ops

## 1. 背景与目标

FR-008/031/051/052/070/304/373 已建成完整的实例文件、配置、插件管理面：路径防穿越（`validatePath`）、文本读取 10MiB 编辑器护栏、改前快照与版本回滚（FileVersion/ConfigVersion）、流式上传下载（O(chunk) 内存、Worker 原子落盘）、插件制品部署（assetId）。这些能力目前只对管理台用户开放。

FR-397 把这套内容运维以强类型 MCP 工具开放给 scoped Agent，同时执行**控制与数据分离**：MCP 只承载小文本与元数据，大文件（jar/世界包/任意二进制）经短时单用途**流式 HTTP 传输票据**走既有数据面。目标场景：Agent 修改房间配置、部署小游戏插件、重启后核对日志，为 500 Bot 战役准备目标服。

## 2. 需求（要什么）

### 2.1 新增 action 与工具清单

全部 `V1Allowed=false`。resource=instance，实例目标经 `AuthorizeInstanceAction` 解析与授权。

#### 文件域（capability=`instance.content`；只读部分 `instance.read`）

| MCP 工具 | action | capability | 操作 | 复用 service |
|---|---|---|---|---|
| `file_list` | `agent.file_list` | `instance.read` | read | `FileService.ListFiles` |
| `file_check_access` | `agent.file_check_access` | `instance.read` | read | `FileService.CheckPathAccess` |
| `file_read_text` | `agent.file_read_text` | `instance.read` | read | `FileService.ReadFile`（含文本判定与上限，见 2.3） |
| `file_write_text` | `agent.file_write_text` | `instance.content` | write | `FileVersionService.SnapshotBeforeWrite` + `FileService.WriteFile` |
| `file_rename` | `agent.file_rename` | `instance.content` | write | `FileService.RenameFile` |
| `file_chmod` | `agent.file_chmod` | `instance.content` | write | `FileService.ChmodPath` |
| `file_delete` | `agent.file_delete` | `instance.content` | destructive | `FileService.DeleteFile` + 精确确认 `confirmPath` |
| `file_versions` | `agent.file_versions` | `instance.read` | read | `FileVersionService.Versions` |
| `file_diff` | `agent.file_diff` | `instance.read` | read | `FileVersionService.Diff` |
| `file_rollback` | `agent.file_rollback` | `instance.content` | write | `FileVersionService.Rollback` |

#### 配置域（capability=`instance.configure`）

| MCP 工具 | action | capability | 操作 | 复用 service |
|---|---|---|---|---|
| `config_discover` | `agent.config_discover` | `instance.read` | read | `ConfigService.Discover` |
| `config_read` | `agent.config_read` | `instance.read` | read | `ConfigService.Read` |
| `config_write_text` | `agent.config_write_text` | `instance.configure` | write | `ConfigService.Write` |
| `config_write_fields` | `agent.config_write_fields` | `instance.configure` | write | `ConfigService.WriteFields` |
| `config_cross_check` | `agent.config_cross_check` | `instance.read` | read | `ConfigService.CheckCrossFile` |
| `config_versions` | `agent.config_versions` | `instance.read` | read | `ConfigService.Versions` |
| `config_diff` | `agent.config_diff` | `instance.read` | read | `ConfigService.Diff` |
| `config_rollback` | `agent.config_rollback` | `instance.configure` | write | `ConfigService.Rollback` |

#### 插件域（capability=`instance.content`）

| MCP 工具 | action | capability | 操作 | 复用 service |
|---|---|---|---|---|
| `plugin_list` | `agent.plugin_list` | `instance.read` | read | `PluginService.List` |
| `plugin_deploy_from_asset` | `agent.plugin_deploy_from_asset` | `instance.content` | write | `PluginService.BatchDeploy`（单实例单 asset 形态） |
| `plugin_toggle` | `agent.plugin_toggle` | `instance.content` | write | `PluginService.Toggle` |
| `plugin_delete` | `agent.plugin_delete` | `instance.content` | destructive | `PluginService.Delete` + 精确确认 `confirmName` |

`plugin_deploy_from_asset` 只接受既有制品 `assetId`（`model.Asset` 表），不接受任何文件字节；asset 不存在返回收敛错误。**制品上传/管理不开放**（属管理面 + FR-397 大文件票据上传路径）。

#### 传输票据域（capability=`instance.content`）

| MCP 工具 | action | capability | 操作 | 说明 |
|---|---|---|---|---|
| `file_issue_transfer_ticket` | `agent.file_issue_transfer_ticket` | `instance.content` | write | 签发短时单用途上传/下载票据 |

### 2.2 流式 HTTP 传输票据（控制与数据分离核心）

MCP 不承载大文件字节。Agent 需要上传 jar/世界包或下载大文件时：

1. 经 `file_issue_transfer_ticket` 申请，参数：`id`（实例）、`direction`（upload/download）、`path`（目标路径，过 `validatePath`）。
2. CP 用 HMAC-SHA256 签发票据，票据摘要持久化到 `agent_transfer_tickets`；消费时以条件更新原子标记，重启或多进程部署下仍保持一次性语义：
   - claims：`tokenId`（Agent Token ID）、`instanceId`、`direction`、`path`、`expiresAt`；
   - TTL 5 分钟；单次消费（`onetimetoken.Store`，consume 即作废；上传失败可重新申请）。
3. 新 HTTP 数据面端点（无需 JWT/Agent Header，票据即凭据）：
   - `PUT /api/v1/agent-transfer/upload?ticket=...`：请求体为原始字节流 → 复用 `FileService.UploadFile`（O(chunk)、Worker 原子落盘）；上传前执行 `SnapshotBeforeWrite`（复用 FR-051 语义）。
   - `GET /api/v1/agent-transfer/download?ticket=...`：流式返回 → 复用 `FileService.DownloadFile`。
4. 消费时服务端校验：签名、过期、未消费、Token 未吊销（实时查库）、实例归属重验（票据内 instanceId 的当前归属仍在签发时的授权范围）。任一不符 → 401/403 + 中文错误，不落盘。
5. 票据签发与消费均写调用流水（action：`agent.file_issue_transfer_ticket` / `agent.file_transfer_consume`，capability=`instance.content`；consume 是流水标签不是可授权 action）。

### 2.3 文本护栏

- `file_read_text`：service 返回后判定二进制（含 NUL 或非 UTF-8 比例阈值——复用/对齐 Worker 判定），二进制 → 中文错误引导走票据下载；正文上限 512KiB（MCP 响应体量护栏，超限引导票据）。
- `file_write_text`：content 参数上限 512KiB（超限 400 语义错误引导票据上传），路径过 `validatePath`。
- `config_read`/`config_write_text`：沿用 ConfigService 既有语义；MCP 层同样加 512KiB 护栏（配置文件本就应远小于此）。
- 上限常量集中定义（`mcpTextContentMaxBytes`），中文错误信息明确写出上限与替代路径。

### 2.4 危险确认

- `file_delete`：必填 `confirmPath`，与 `path` 精确一致才执行（防参数错位误删）。
- `plugin_delete`：必填 `confirmName`，与插件名精确一致。
- 复用 FR-396 的 `RequiresConfirm` 目录机制（若 FR-396 未先合入，本 FR 自带等价实现，合并时收敛为一份——见风险）。

### 2.5 范围外

- 归档浏览/反编译/全文搜索（`ArchiveEntries`/`Decompile`/`Search`）——500 Bot 战役无需，YAGNI。
- 批量下载打包 zip、多实例插件批量部署（管理面能力保留）。
- 制品库管理（上传/删除 asset）。
- 目录递归删除的专门工具（`file_delete` 语义与管理面 DELETE 一致，由 Worker 决定目录行为）。
- MCP 内联 base64 大文件（永久不做）。

## 3. 设计（怎么做）

### 3.1 action 目录与工具注册

- `agent_capability.go` 登记 §2.1 全部 action（含 `agent.file_transfer_consume` 仅作流水标签、不注册工具——`DescribeAgentAction` 可见但 `CanDiscover` 恒拒绝，或单列常量不进目录，实现取简）。
- MCP 侧新文件：`tools_file.go`、`tools_config.go`、`tools_plugin.go`（依赖 FR-396 的 `toolSpec.Exec` 注册表；若并行期该重构未合入，先在本分支用等价注册表实现，合并时对齐）。
- `ToolDeps` 增：`File *FileService`、`FileVersion *FileVersionService`、`Config *ConfigService`、`Plugin *PluginService`、`Transfer *AgentTransferTicketService`。

### 3.2 票据服务

新文件 `internal/controlplane/service/agent_transfer_ticket.go`：

```text
type AgentTransferTicketService struct {
    signer  // HMAC，密钥经 DeriveXxxSecret 从主密钥域分离（对齐 BotLoadPlanTokenSigner 先例）
    db      // 票据摘要与消费状态；仅保存 SHA-256 摘要，不保存票据明文
    agent   *AgentTokenService   // 消费时重验 Token 有效性与实例归属
    file    *FileService
}
Issue(p *AgentPrincipal, instanceID uint, direction, path string) (ticket string, expiresAt time.Time, error)
Consume(ticket string) (*TransferClaims, error)   // 校验签名/TTL/一次性/Token 未吊销/归属重验
```

- HTTP handler：`internal/controlplane/router/agent_transfer.go`，注册在无 JWT 中间件的组（票据即凭据）；上传走请求级 ctx 流式转发。
- 密钥来源与 CP 主密钥装配一致（main.go 注入），无主密钥时票据功能禁用并返回中文「票据功能不可用」。

### 3.3 写路径版本快照

`file_write_text` 与票据上传都先 `SnapshotBeforeWrite`（FR-051 语义：已存在才快照）；`config_write_*`/`config_rollback` 由 ConfigService 内部已有版本机制处理，不重复。

### 3.4 错误语义

- scope 外/不存在：收敛 `ErrAgentForbidden`。
- 路径穿越：`validatePath` 中文错误原样返回。
- 体积超限/二进制：明确中文引导（「内容超过 512KiB/检测为二进制，请使用 file_issue_transfer_ticket 走流式传输」）。
- 票据无效的所有形态（过期/已消费/签名错/Token 吊销/归属变化）归一为 403 + 「票据无效或已失效」（不区分泄露内部状态）。

## 4. 任务拆分

- [ ] 失败测试先行：action 目录 V1/V2 矩阵、文本上限、二进制拒绝、确认参数、票据过期/复用/改路径/吊销后消费全部拒绝。
- [ ] `agent_capability.go` 登记 action。
- [ ] `AgentTransferTicketService` 实现 + 单测（签发/消费/一次性/归属重验）。
- [ ] HTTP 数据面端点 `agent-transfer` upload/download + router 测试。
- [ ] MCP `tools_file.go`/`tools_config.go`/`tools_plugin.go` + InputSchema + 契约测试。
- [ ] main.go 装配（票据密钥派生 + ToolDeps）。
- [ ] 文档同步：PRD 状态、ARCHITECTURE、API（新增 agent-transfer 端点）、`docs/specs/cp-mcp-server/spec.md` 工具表、CHANGELOG 末尾追加。
- [ ] 真机链路：V2 Token（instance.content+configure+read+observability.read）走通「读配置→改字段→票据上传插件 jar→启用→重启→日志确认」，证据存 `.tmp/acceptance-fr397-*.txt`。

## 5. 验收标准

1. **无大字节经 MCP**：所有工具拒绝超限内容；插件部署只认 assetId；代码审查无 base64 大内容路径。
2. **票据安全**：过期、复用、改实例/路径、Token 吊销后消费全部失败（测试矩阵）；票据绑定 direction，上传票据不能用于下载。
3. **流式与原子性**：票据上传沿用 `UploadFile` O(chunk) 与 Worker 原子落盘；下载沿用流式 RPC（回归测试）。
4. **版本与回滚**：MCP 文本写与票据上传覆盖已存在文件前有改前快照；`file_rollback`/`config_rollback` 可恢复（测试）。
5. **危险确认**：delete/plugin_delete 缺确认或不匹配即拒 + 写流水。
6. **scope 硬边界**：实例目标全部经归属解析授权；scope 外收敛错误；路径穿越拒绝。
7. **tools/list 裁剪**：仅 `instance.content`/`instance.configure`/`instance.read` 持有者可见对应工具（矩阵测试）。
8. **流水**：读写与票据签发/消费均入调用流水。
9. **回归**：`go test ./internal/controlplane/...` 全绿；管理面文件/配置/插件行为零变化。
10. **真机**（需用户确认通过）：§4 最后一项闭环证据。

## 6. 风险 / 待定

- **与 FR-396 的注册表重构并行冲突**：两分支都动 `tools.go` 骨架。缓解：FR-396 负责重构骨架，本 FR 的新工具文件只「追加注册」；集成时以 FR-396 骨架为准做一次对齐（整合顺序建议 FR-396 先落 main）。
- **票据端点绕过 Agent Header**：票据自身含全部授权上下文且一次性；端点不接受任何路径/实例参数（全部从票据 claims 取），无参数注入面。
- **`file_read_text` 二进制判定与 Worker 不一致**：以 CP 侧统一判定为准（NUL/UTF-8 阈值），文档写明可能与编辑器行为有细微差异。
- **Token 吊销与已签票据**：消费时实时查库校验 Token 状态，吊销即失效——无需票据撤销列表。
