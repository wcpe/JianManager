# 二进制 / Beacon 版本管理与受控升级（FR-468）

> 状态：📋 计划　·　关联 PRD：FR-468　·　依赖：FR-441、FR-442、FR-409、FR-051、FR-342　·　关联 ADR：ADR-090

## 1. 背景与目标

FR-441/442 让平台能搭建通用二进制与 Beacon，但**版本维度是缺失的**：

- 搭建时来源由请求直供（`BinarySource`）或 Beacon 预设自动解析（`resolveBeaconPresetSource`），版本选择是「隐式」的——`latestBeaconAsset` 只按 `id DESC` 取最新，没有「当前是哪个版本」的概念。
- **重建不冻结版本**：`rebuildBinaryInstance`（`provision_binary.go:841`）明确注释「重新按优先级解析，而非复用上次选中的具体制品」。这是搭建时的合理设计（制品库优先），但对**运行中的实例**意味着：重建可能静默把实例升级/降级到另一个版本，运维无法预期。
- 无版本比对、无升级入口、无回滚。改版本只能靠运维记住旧文件名、手动换文件。

对照**ServerProbe 已有成熟范式**（FR-409，`service/artifact_version.go`）：`ArtifactVersionService` 提供 `InstanceProbeVersion`（实例显式覆盖）/ `SetInstanceProbeVersion` / `ResolveInstanceProbeVersion`（实例 → Worker → 全局三级解析），并有 `probe_update.go` 的 `Update` 受控升级。FR-468 就是把同一套「版本管理 + 受控升级 + 回滚」范式**移植到通用二进制/Beacon**。

**范围内**
- 记录并展示**实例当前二进制版本**（版本号 + sha256 + 落盘文件名）
- **受控升级**：把实例升到指定版本（从制品库选版本）
- **回滚到上一版本**（升级前状态可退回）
- 搭建/重建时**冻结版本选择**（不再隐式漂移）

**不做（范围外）**
- 不做二进制自动升级（升级始终是运维显式动作）
- 不做灰度/批量编排（单实例语义；批量走实例批量接口）
- 不改变 ADR-090 的「可选协同」边界（版本管理纯本机，不依赖 Beacon）
- 不改 FR-182 的 CP/Worker 自更新回滚（那是平台自身二进制）

## 2. 设计

### 2.1 版本真源（决策：复用制品库 `Asset`，不新建版本表）

二进制版本 = 制品库中一条 `model.Asset`（内容寻址，`SHA256` 去重，`Version`/`Filename` 字段已是版本语义）。**不新建版本表**，理由：制品库已是二进制的唯一内容真源（ADR-011），`Asset` 天然满足「同内容一份、版本可查、可分发」；再建一套会形成双真源。

需要新增的是**「实例 ↔ 版本」绑定**，即「该实例当前跑的是哪个 asset」。复用 ServerProbe 的实例绑定模式（`InstanceProbeVersion` 存在实例扩展表）：

```go
// model/instance_binary_binding.go（新增，或复用既有实例扩展表）
type InstanceBinaryBinding struct {
    ID           uint   `gorm:"primaryKey"`
    InstanceID   uint   `gorm:"not null;uniqueIndex"`
    // CurrentAssetID 实例当前生效的二进制制品。
    CurrentAssetID uint `gorm:"not null"`
    // PreviousAssetID 上一次升级前的制品，供「回滚到上一版本」（0=无可回滚）。
    PreviousAssetID uint `gorm:"default:0"`
    // CurrentSHA256 / CurrentVersion 冗余快照，便于列表展示与漂移检测。
    CurrentSHA256  string `gorm:"type:char(64)"`
    CurrentVersion string `gorm:"type:varchar(128)"`
    UpdatedAt      time.Time
}
```

### 2.2 搭建 / 重建时冻结版本

- **搭建**（`ProvisionBinaryAsync` / `provisionBeaconPresetAsync`）：解析到具体 `Asset`（或 URL）后，写入 `InstanceBinaryBinding.CurrentAssetID`。
  - `kind=asset` / Beacon 制品库来源：直接绑定该 `Asset.ID`。
  - `kind=url` / `node_file`：无制品库记录，`CurrentAssetID=0`，仅记录落盘文件名 + 计算出的 sha256（展示「当前版本：未知来源」）。
- **重建**（`rebuildBinaryInstance`）：改为**读取绑定**而非重新解析——
  - 若 `CurrentAssetID != 0`：取件固定为该 asset（`IssueBinaryDownloadToken`），**不再 `latestBeaconAsset` 漂移**；
  - 保留既有「显式指定来源时以请求为准」的逃生口（运维可覆盖）。
  - 语义变更点：ADR-090 曾注明「重建重解析是为让制品库新版本生效」——现在这层「新版本生效」由**受控升级**（§2.3）显式承担，重建回归「恢复原状」职责。**这是本 FR 的关键决策**，需在 ADR 补充说明。

### 2.3 受控升级（决策：升级即换绑定 + 换文件，走任务）

`BinaryVersionService.Upgrade(instanceID, targetAssetID, operatorID)`：
1. 校验目标 `Asset` 是合法二进制（尺寸 >0、非 lost、文件名合法，复用 `isBeaconLinuxBinaryName` / `validBinaryFilename`）；
2. 校验实例当前状态（运行中需先停服，或复用快照回滚的停服编排）；
3. 记录 `PreviousAssetID = CurrentAssetID`（回滚点）；
4. 下发取件到工作目录（复用 FR-441 的 Worker `FetchBinary` 通道），落盘名按目标 `Asset` 名；
5. 若落盘名变化 → 更新 `startCommand`（复用 `deriveBinaryStartCommand`），与 `rebuildBinaryInstance:865-871` 同口径（用户显式命令不被改写）；
6. 更新绑定 `CurrentAssetID/CurrentSHA256/CurrentVersion`，写审计 `instance.binary_upgrade`。

任务类型：`TaskKindBinaryUpgrade = "binary_upgrade"`。阶段：`停服 → 取目标版本 → 校验 → 落盘 → 更新启动命令 → 完成`。

**升级前自动留档**：可在升级前触发一次**实例快照**（FR-466，`pre_rollback` 语义），使「升级 → 回滚」与「回滚 → 退回」形成完整可逆闭环。跨 FR 协同（本 FR 不强制，标为推荐）。

### 2.4 回滚到上一版本

`BinaryVersionService.Rollback(instanceID)`：
- 要求 `PreviousAssetID != 0`（否则 `ErrNoPreviousBinaryVersion`）；
- 取件 `PreviousAssetID` 到工作目录，恢复 `startCommand`（若曾改），写审计 `instance.binary_rollback`；
- 交换绑定：`Current ↔ Previous`（回滚本身也可再回滚，语义对称）。

> 决策：只做**一级回滚**（上一版本），不做任意版本回退。理由：任意版本=再执行一次 `Upgrade`（选目标 asset 即可），无需专门入口；一级回滚覆盖「升错了，退回去」的高频场景，语义最清晰。

### 2.5 版本漂移检测

- 列表页展示 `CurrentVersion` + 制品库是否有更新版本（`latestBeaconAsset` 或同 package 更高 `id`），给出「可升级到 X」提示。
- 巡检（可选）：发现工作目录二进制的实际 sha256 与绑定 sha256 不一致（人工换文件）→ 标注「版本漂移」，告警可选。复用 FR-399 的资源快照/文件读取通道计算 sha256。

### 2.6 API / MCP

- REST（新 `router/binary_version.go`）：`GET /instances/:id/binary-version`（当前版本 + 可升级列表 + 可回滚性）、`POST /instances/:id/binary-upgrade`（body: `assetId`）、`POST /instances/:id/binary-rollback`。
- MCP（`mcp/tools_instance.go`）：`instance_binary_version_get`（读）、`instance_binary_upgrade` / `instance_binary_rollback`（写，受 `instance.write`）。

## 3. 任务拆分

- [ ] `model/instance_binary_binding.go` + AutoMigrate + 单测
- [ ] 搭建/Beacon 预设路径写入绑定（`provision_binary.go`：`binaryProvisionRunner` / `createBeaconInstance`）+ 单测
- [ ] `rebuildBinaryInstance` 改为读绑定冻结版本（保留显式覆盖逃生口）+ 单测（不漂移、随绑定、显式覆盖生效）
- [ ] `service/binary_version.go`：Upgrade（取件 + 落盘名/startCommand 更新 + 绑定切换 + 审计）+ 单测
- [ ] `service/binary_version.go`：Rollback（一级回滚 + 绑定交换）+ 单测（无可回滚、对称性）
- [ ] `model/task.go` 新增 `TaskKindBinaryUpgrade`；阶段文案
- [ ] 漂移检测（sha256 比对）+ 可升级提示
- [ ] `router` 三端点 + 权限 + 审计动作；MCP 三工具
- [ ] 前端实例详情「二进制版本」区：当前版本 / 升级选择 / 回滚按钮（二次确认）+ i18n
- [ ] 文档同步：ARCHITECTURE、API.md、PRD 状态、CHANGELOG；ADR-090 补「重建冻结版本 + 受控升级」决策

## 4. 验收标准

- 单测全绿：绑定写入/读取、重建冻结版本（不漂移）、升级（绑定切换 + startCommand）、一级回滚（对称）、无可回滚报错、显式覆盖逃生口
- **真机（要真机过）**：① 从制品库搭建 Beacon → 显示当前版本；升级到另一版本 → 落盘/启动命令/绑定三者一致；回滚到上一版本 → 恢复旧文件与旧启动命令；② **重建不再漂移**：制品库新增更高版本后对既有实例执行「重建」→ 仍取绑定版本，**不静默升级**（对照 ADR-090 旧行为）；③ 升级/回滚全程任务中心可见、审计可查、站内信可达；④ 落盘名不变与变两种路径的 `startCommand` 处理均正确；⑤ 无 `kind=asset` 绑定的 url/node_file 实例升级入口给出明确「无制品库版本」提示
- 横切：FR-441/442 搭建闭环不回归；未部署 Beacon 时无影响

## 5. 风险 / 待定

- **ADR-090 语义修订**：重建从「重解析」改为「冻结绑定」是行为变更，可能与既有运维习惯冲突（靠重建吃新版本）。缓解：保留显式覆盖逃生口 + 明确「升级」为主路径。需 ADR 记录。
- **url/node_file 来源无版本**：无制品库记录，无法纳入版本管理。首版只展示 sha256、升级入口提示先入库；待定是否自动把 url 下载物入库。
- **磁盘占用**：升级/回滚在工作目录留旧文件可能不清理。首版**保留**，由 FR-466 快照/清理策略统一处理。
- **与快照（FR-466）协同**：升级前是否强制建快照。首版推荐但不强制，避免两条链路耦合过深。
- **多架构二进制**：当前 filename 硬编码 `linux-amd64`（`isBeaconLinuxBinaryName`）。跨架构（arm）需扩展识别，首版不覆盖。
