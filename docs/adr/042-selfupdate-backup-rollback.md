# ADR-042: 自更新升级前备份与一键回滚 + 滚动预发布 nightly→latest

- **日期**: 2026-06-27
- **状态**: accepted
- **上下文**: FR-081（面板自更新）+ FR-175（对接 GitHub Releases）让 CP/Worker 能在线升级二进制，但升级是**单向不可逆**的——升到有问题的新版本后，平台内没有退路，运营者只能手动找回旧二进制、手动替换、手动重启（且 Windows 上替换运行中 exe 还有坑）。生产升级缺一张安全网。本 ADR 由 FR-182 创建，给自更新补「升级前自动备份 + 一键回滚到上一版」；同时对齐一处发布渠道命名：FR-173 的滚动预发布固定 tag 由 `nightly` 改为 `latest`，自更新 `prerelease` 渠道相应消费。

## 决策

### 1. 升级前备份当前二进制（单份，复制非移动）

- 升级（CP 自升 / Worker 升级）在 `ReplaceExecutable` **之前**，把**当前**可执行文件备份到数据根 `cache/selfupdate-backup/<component>/`，内含 `binary`（备份的可执行文件）+ `meta.json`（`{version, sha256, backedUpAt}`）。`<component>` ∈ `control-plane` | `worker`。
- **每组件只留一份**：再次升级覆盖上一份。回滚只回到「升级前那一版」，不做多版本档案（YAGNI——运营要的是「刚升坏了退回去」，不是任意历史版本时光机）。
- **复制而非 rename**：当前二进制仍在运行（持有该文件），不能把它 move 走；故复制到备份目录。备份落地用「临时文件 + rename」保证原子（不留半截备份）。
- **备份失败不阻断升级**：备份是增益，不是升级的前置硬依赖；备份失败仅 `slog.Warn`，升级照常（但这次升级就没有退路）。绝大多数情况备份成功。

### 2. 一键回滚（校验备份 sha → 换回 → 重启）

- `Rollback(component, root)`：读 `meta.json`（无 → `ErrNoBackup`）→ 校验备份 `binary` 的 sha256 与 `meta.json` 一致（**防备份在磁盘上被损坏 / 篡改后把坏二进制换上去**）→ 复制 `binary` 到临时文件 → `ReplaceExecutable` 换回当前位置 → 返回 meta（含回滚到的 `version`）。
- **回滚源也 sha 校验**：与升级「不信内容只信指纹」同一安全心智（ADR-011 / ADR-036 §2）。备份 sha 不符即拒绝回滚、不替换。
- **复制备份而非移走**：回滚时把 `binary` 复制出来替换，**备份原地保留**，支持重复回滚 / 事后排查。
- **不在 `Rollback` 内重启**：与升级一致——替换成功后由调用方**异步延迟重启**（先让 HTTP 202 / gRPC 响应回到对端，再重启进程），避免连接被切导致对端误判失败。daemon 模式下回滚 / 重启只动主进程，wrapper 子进程（持 Java 游戏服）独立存活，重启后 `RecoverDaemonInstances` 重连（ADR-003），游戏服不掉。
- **首次部署无备份**：`BackupInfo` 返回无备份 → 前端回滚按钮禁用、后端 `ErrNoBackup`。符合预期（没升过级就没有「上一版」可退）。

### 3. 节点回滚复用 Worker 升级 RPC（加 `action` 字段，加性）

- **不新增 RPC**：`UpgradeWorkerRequest` 加性新增 `string action = 5`（空 / `upgrade` = 既有升级；`rollback` = 回滚本地备份，忽略 `download_url`/`sha256`）。`GetVersionResponse` 加性新增 `string backup_version = 4`（Worker 本地备份版本，供 CP 检查更新透出节点 `backupVersion`）。
- **向后兼容**：旧 CP 不传 `action` → Worker 走默认升级路径，行为与字段未加时完全一致（protobuf 加字段是向后兼容变更）。回滚走本地备份，**不经 CP 下发任何 url/sha**（节点自己有备份）。
- 复用既有「替换→异步延迟重启」原语与 `doRestart`/`SetExecutablePath`/`SetRestartFunc` 测试注入点。

### 4. CP 编排回滚 + 路由 + 审计 + RBAC

- CP 服务加 `RollbackControlPlane`（回滚 CP 自身）/ `RollbackNode`（经 gRPC 令节点回滚）。`ComponentStatus` 加性新增 `backupVersion`（CP 从本地 `BackupInfo` 取、节点从 `GetVersion.backup_version` 取）。
- HTTP：`POST /self-update/control-plane/rollback`、`POST /self-update/nodes/:id/rollback`，挂平台管理员组（RBAC 同升级），写审计 `self_update.control_plane_rollback` / `self_update.node_rollback`（detail 仅版本 / 节点元数据，绝不含路径 / 凭据）。
- 错误码：`409 UPDATE_NO_BACKUP`（无备份）、`503 NODE_OFFLINE`、`502 UPDATE_FAILED`（回滚失败，如备份 sha 不符）。

### 5. 前端：notes 独立说明块 + 回滚按钮

- `notes`（release body）由「单行附在版本号后」改为**独立可读说明块**（多行 release 说明完整、预格式展示）。
- CP 卡片与每个节点行新增「回滚 v{backupVersion}」按钮，走 `DangerConfirm`（scope=platform 二次确认，FR-059）；`backupVersion` 为空时按钮禁用。

### 6. 滚动预发布 tag `nightly` → `latest`（取代 ADR-036 §3/§7 该命名）

- FR-173 发布管线的**滚动预发布固定 tag** 由 `nightly` 改名为 `latest`（`.github/workflows/release.yml` 的 `release_tag` 元数据、清理既有资产、发布步骤 `tag_name`/`name`）。语义不变：仍是 push `master` 出 / 覆盖的预发布、只留最新产物。
- 自更新 `prerelease` 渠道端点相应由 `/releases/tags/nightly` 改为 `/releases/tags/latest`（`selfupdate_github.go`）。
- `update.github_repo` 默认值由 `wcpe/jianmanager` 改为 `wcpe/JianManager`，对齐真实仓库名大小写（GitHub 仓库名大小写不敏感于路由但展示 / clone 一致更稳）。
- 本节**取代 ADR-036 §3「滚动预发布固定 tag `nightly`」与 §7「prerelease → `/releases/tags/nightly`」的命名**（ADR-036 标该两处 `superseded-by ADR-042`）；ADR-036 的产物命名 / 校验 / 渠道其余契约不变。

## 理由

- **备份 + 回滚是升级的对偶**：有「上去」就该有「下来」。单份备份用最小存储成本（一个二进制）换「升坏了能退」的确定性退路，覆盖绝大多数运维事故。
- **sha 校验回滚源**：备份在磁盘上可能被损坏 / 误改，不校验就回滚等于可能把坏二进制换上去——与升级同样「只信指纹」。
- **复用 Worker 升级 RPC 加字段**：回滚与升级的「替换 + 重启」机制完全相同，差别只是「新二进制从哪来」（下载 vs 本地备份）。加 `action` 字段比新开一个 `RollbackWorker` RPC 更省、更内聚，且 protobuf 加字段天然向后兼容。
- **复制而非移动备份**：当前二进制在运行不能移走；回滚后保留备份支持重复回滚 / 排查。
- **不自动回滚**：自动健康探测回退是更大的命题（探测口径、回滚风暴），本 FR 守住「手动、可控」边界（同 FR-081 不自动升级 / 不自动放量）。
- **nightly→latest**：用户明确要求统一命名为 `latest`，与「最新可用预发布」语义直观一致；这是契约命名微调，借 ADR 取代留痕，避免与 ADR-036 文字冲突。

## 后果

- `internal/platform/selfupdate` 新增 `BackupCurrent`/`Rollback`/`BackupInfo`/`BackupMeta`/`ErrNoBackup`；升级链路（CP `UpgradeControlPlane`、Worker `UpgradeWorker`）替换前多一步备份。
- `proto/worker.proto` 改 `UpgradeWorkerRequest`（+`action`）与 `GetVersionResponse`（+`backup_version`），`make proto` 重新生成 `proto/workerpb`。
- CP `service`/`router` 新增回滚编排与两个端点 + 错误码 `UPDATE_NO_BACKUP` + 两条审计动作；`ComponentStatus`/`CheckResult` 加性新增 `backupVersion`。
- 前端 `SystemUpdatePage` notes 改说明块 + 回滚按钮；`api/selfUpdate.ts` 加 `backupVersion`/`source` 类型与回滚 mutation。
- `.github/workflows/release.yml` 滚动预发布 tag 改 `latest`；首次跑会新建 `latest` 预发布，既有 `nightly` release 需一次性手动清理（运维动作，文档注明）。
- ADR-036 §3 / §7 的 `nightly` 命名标 `superseded-by ADR-042`。
- 真机两条待用户确认：CP / Worker 升级后一键回滚成功（含 Windows 替换运行中 exe、daemon 游戏服不掉）；真 CI 出 `latest` 预发布并被 `prerelease` 渠道消费。本地以 httptest + 临时文件覆盖单测。

## 替代方案

- **多版本备份档案（保留 N 份历史二进制，可回滚到任意一版）**：能力更强，但存储成本线性增长、UI / API 要管版本列表，且运维实际诉求是「退回刚才那一版」；放弃（只留一份，多版本档案后续按需另立 FR）。
- **新开 `RollbackWorker` gRPC RPC**：语义更显式，但与 `UpgradeWorker` 的替换 / 重启逻辑高度重复，要再走一遍 proto + 生成 + 测试样板；放弃（加 `action` 字段复用，更内聚且向后兼容）。
- **回滚 = 重新下载旧版本制品**：依赖「旧版本制品在 release 还在」（latest-only 渠道下旧 release 可能已被覆盖 / 不可寻址），且要联网；本地备份回滚离线可用、不依赖远端仍保留旧产物；放弃（本地备份更可靠）。
- **升级前备份失败即阻断升级**：更「安全」，但把备份从增益变成升级的硬前置，磁盘满 / 权限问题会卡住升级；放弃（备份失败仅告警，升级照常——代价是这次升级无退路）。
- **保留 `nightly` 命名**：维持 ADR-036 原状，但用户明确要 `latest`；放弃（按用户决策对齐，ADR 取代留痕）。

## 关系

- **ADR-003（守护进程 Wrapper）**：回滚 / 重启只动 Worker 主进程，daemon wrapper 子进程独立存活、重启后重连——与升级同一隔离前提，游戏服不掉。
- **ADR-011（制品库）/ ADR-036 §2（checksums 校验）**：回滚源 sha256 校验与升级完整性校验同源（内容寻址 / 只信指纹）。
- **ADR-020 §4（自更新来源，已 superseded-by ADR-036）**：本 ADR 不改来源，只加「升级前备份 + 回滚」对偶能力。
- **ADR-036（发布管线与 GitHub Releases 契约）**：本 ADR 取代其 §3 / §7 的滚动预发布 `nightly` 命名为 `latest`，其余产物命名 / 校验 / 渠道契约不变。
- **ADR-037（出站代理）**：节点回滚走本地备份**不经网络**，无需代理；CP / Worker 升级下载仍经 ADR-037 代理（不变）。
- **FR-081（面板自更新）/ FR-175（对接 GitHub Releases）**：本 ADR 的落地 FR-182 在其链路上增量加备份 / 回滚与渠道命名对齐，升级 / 下载 / 校验主链路零行为变更。
