# FR-182 spec：自更新体验增强（更新内容展示 + 一键回滚 + 单版本备份）

> 状态：待审　·　关联 PRD：FR-182（增强 FR-081）　·　关联 ADR：036 §8（nightly→latest 收口）　·　分支：feature/fr-182-selfupdate

## 1. 背景与目标

FR-081 自更新已交付（CP/Worker 二进制热替换 + 平滑重启），但：① 检查更新只把 release 说明当一行 `— notes` 塞在版本号后，看不清更新了啥；② 升级后旧二进制即丢（Unix unlink、Windows 的 `.old` 下轮即删），**无法回滚**；③ 顺带收口 FR-173/175 的 `nightly`→`latest` 命名（ADR-036 §8）。

**目标**：升级前自动备份当前二进制（只留一份）、提供一键回滚上一版、检查更新时完整展示更新内容。

## 2. 范围

### 范围内
- **更新内容展示**：SystemUpdatePage 把 `CheckResult.notes`（GitHub release body，即 CHANGELOG 段）渲染为可读区域（标题 + 正文，长则可折叠），CP 与各节点升级前可见目标版本说明。
- **单版本二进制备份**：CP/Worker 在 `ReplaceExecutable` 前，把当前二进制连同版本号备份到数据根 `cache/`（如 `cache/selfupdate-backup/<component>.bin`），**只保留一份**（新备份覆盖旧），带 sidecar 元数据（version、时间、sha256）。
- **一键回滚上一版**：CP 自回滚（备份→替换→重启）；Worker 回滚经 CP gRPC 编排（mirror 升级链路）。SystemUpdatePage 每组件（CP + 各节点）一个「回滚到 vX」按钮，DangerConfirm + scope=platform；无备份则禁用并提示。
- **FR-173/175 收口**（同 worktree 小改）：`release.yml` 与 `selfupdate_github.go` 把 `nightly`→`latest`；`github_repo` 默认校准 `wcpe/JianManager`。

### 非目标
- 不做多版本备份/任意历史回滚（只留上一版，单备份）。
- 不做自动定时检查/自动升级（沿用 FR-081 手动边界）。
- 不做二进制签名（sha256 完整性即范围，ADR-036 §2）。

## 3. 设计

### 3.1 备份与回滚（`internal/platform/selfupdate`）
- `BackupCurrent(component, version, dataRoot) error`：把 `os.Executable()` 复制到 `cache/selfupdate-backup/<component>.bin` + 写 `<component>.json`（version/time/sha256）；覆盖旧备份（单版本）。
- `Rollback(component, dataRoot) error`：校验备份 sha256 → `ReplaceExecutable(target, backup)` → `Restart`；无备份返回新错误 `ErrNoBackup`。
- 升级流程（CP `UpgradeControlPlane` / Worker `selfupdate_ops`）在 `ReplaceExecutable` 前调用 `BackupCurrent`。

### 3.2 编排（CP service + router + worker gRPC）
- CP：`SelfUpdateService.RollbackControlPlane()`（自回滚）、`RollbackNode(nodeID)`（经 gRPC 下发 Worker 回滚）。
- Worker gRPC：`selfupdate_ops` 加回滚 op（**优先复用既有 selfupdate RPC 加 `action=rollback` 字段，加性不破**；或新增 `Rollback` RPC，落地定）。
- 路由：`POST /self-update/control-plane/rollback`、`POST /self-update/nodes/:id/rollback`（仅平台管理员 + 审计 FR-015）。
- 组件状态加 `backupVersion`（有备份时透出，前端据此决定回滚按钮可用性 + 文案）。

### 3.3 更新内容展示（`SystemUpdatePage.tsx`）
- `result.notes` 从一行 `— notes` 改为独立说明块（`<pre>` / 轻 markdown，长内容 `max-h` + 主题化滚动条 FR-176）。
- 组件卡 / 节点行加「回滚 vX」按钮（`backupVersion` 非空可用）+ DangerConfirm。

### 3.4 nightly→latest（ADR-036 §8）
- `release.yml`：`release_tag` 兜底、`gh release delete`、发布步骤 tag/name `nightly`→`latest`。
- `selfupdate_github.go`：prerelease 端点 `/releases/tags/nightly`→`/releases/tags/latest`。
- `config`：`github_repo` 默认 `wcpe/JianManager`。

## 4. 任务拆分
- [ ] selfupdate：`BackupCurrent` + `Rollback` + sidecar + 单测（备份/回滚/无备份/sha 不符）
- [ ] 升级流程替换前 `BackupCurrent`（CP + Worker）
- [ ] CP service/router 回滚端点（CP 自回滚 + 节点 gRPC 回滚）+ 审计 + RBAC
- [ ] Worker gRPC 回滚 op（加性）
- [ ] SystemUpdatePage：更新内容块 + 回滚按钮 + DangerConfirm + i18n
- [ ] nightly→latest（release.yml + selfupdate_github.go + config 默认）
- [ ] doc-sync：API.md（回滚端点）、ARCHITECTURE（自更新章节）、CHANGELOG、PRD FR-182 行

## 5. 验收
- [ ] 升级前生成单份备份（含 version/sha256）；二次升级覆盖旧备份不堆积
- [ ] CP 一键回滚：替换回上一版二进制 + 平滑重启，重连后版本为旧版
- [ ] 节点回滚：经 gRPC 下发，Worker 替换回滚 + 重启；daemon 模式游戏服不掉
- [ ] 无备份时回滚按钮禁用 + 提示
- [ ] 检查更新完整展示目标版本说明（CP + 节点）
- [ ] nightly→latest：release.yml 产 `latest` 预发布、自更新 prerelease 渠道读 `/releases/tags/latest`
- [ ] i18n（中/英）+ 明暗 + 双主题
- [ ] **真机闸（用户验）**：用户 push 远程跑 CI 出 release → CP 升级 → 回滚 → 节点升级 → 节点回滚 全链路真机过
