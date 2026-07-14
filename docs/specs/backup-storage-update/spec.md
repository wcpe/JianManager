# 功能规格：备份存储配置编辑

> 状态：草拟　·　关联 PRD：FR-338（增强 FR-057）　·　分支：feature/fr-338-backup-storage-update（待建）

## 1. 背景与目标

备份远程存储后端（FR-057）自交付起前后端均无 Update 能力：

- 路由（`internal/controlplane/router/backup_storage.go` `RegisterRoutes`）仅注册 `GET`、`POST /test`、`POST`、`GET /local/stats`、`GET /:id/stats`、`POST /:id/test`、`DELETE /:id`，无 `PUT`；
- service（`internal/controlplane/service/backup_storage.go`）有 Create / List / ListWithStats / Stats / LocalStats / GetByID / Delete / TestCandidate / TestSaved / ResolveSpec / TestConnection，无 Update；
- 前端 `apps/control-plane-web/src/api/backupStorages.ts` 无 useUpdate hook；`BackupStoragesPage.tsx` 表格操作列仅「测试 / 删除」，弹窗仅创建单模式；
- devmock `packages/devmock/src/handlers/domains/backup.ts` 无 PUT handler。

运维改任何配置（换密钥 env 引用、换 endpoint、改名）都只能删了重建；且被备份引用的后端根本删不掉（`ErrStorageInUse` 422），配置错了就永久卡死。目标：补齐编辑链路（后端 PUT + 前端双模式弹窗 + devmock），错误语义与 Create 对齐。

## 2. 需求（要什么）

- 后端 `PUT /backup-storages/:id`：全量替换式更新（body 同 Create），校验复用/对齐 Create（类型枚举、凭证 `${ENV_VAR}` 引用），新增名称冲突预检（排除自身）；`404 / 422` 语义与既有端点一致。
- **类型不可改（拍板）**：S3↔SFTP↔WebDAV 字段语义迥异（bucket/region 仅 S3 有意义、endpoint 含义不同），改型等价删重建；PUT body 的 `type` 必须与现值一致，否则 422。
- **被引用中的存储可编辑**：有备份记录引用（`backups.storage_id`）不加编辑锁——换密钥、换 endpoint 是合法运维场景（这正是本 FR 的主诉求）；风险经 UI 提示承接（见 §3.1、§6）。
- 更新成功后清空 `lastTestAt/lastTestOk/lastTestMessage`：配置已变，旧连通性结论失效。
- 前端：弹窗改 create/edit 双模式（编辑受控填入现值；凭证 env 引用 `${VAR}` 本就非明文，原样回显无泄露）；表格行加「编辑」入口；「测试连接」对编辑草稿仍可用（走既有 `POST /backup-storages/test`，body 形状相同）。
- devmock 补 PUT 镜像（404/type 不可改 422/名称冲突 422/重置 lastTest*）。
- 顺带对称收口（范围内，防止 Create/Update 行为分叉）：Create 撞名称唯一索引现回 500，与 Update 同源的名称冲突预检一并套用 → 422（语义对齐是本 FR 验收点）。
- 不做（范围外）：
  - 改 `type`（明确拒绝，见上）；
  - 凭证明文输入/展示（维持 `${ENV_VAR}` 引用制，config-files.md）；
  - 编辑触发存量备份对象迁移/重传（bucket/prefix 变更不搬数据）；
  - 引用锁或「被引用禁改」策略。

## 3. 设计（怎么做）

### 3.1 后端

**service（internal/controlplane/service/backup_storage.go）** 新增：

```go
// ErrStorageTypeImmutable 存储后端类型不可修改（改型=删重建）。
var ErrStorageTypeImmutable = errors.New("存储后端类型不可修改")
// ErrStorageNameConflict 存储后端名称已存在。
var ErrStorageNameConflict = errors.New("存储后端名称已存在")

// Update 全量更新存储后端配置（FR-338）。类型不可改；校验与 Create 同源；
// 成功后清空 lastTest*（配置已变，旧测试结论失效）。
func (s *BackupStorageService) Update(id uint, st model.BackupStorage) (*model.BackupStorage, error)
```

步骤：

1. `GetByID(id)` → 不存在 `ErrStorageNotFound`（404）；
2. `st.Type != cur.Type` → `ErrStorageTypeImmutable`（422）；
3. 校验与 Create 同源：`ValidBackupStorageType` + `validateCredentialRefs`（注意：既有 `validateCandidate` 是「校验+解析 env+探活」的**测试连接**路径，Update 只复用其中的静态校验两件套，不在保存时强制探活——与 Create 行为对齐，连通性由「测试连接」按钮显式做）；
4. 名称冲突预检（**新逻辑**，Create 现无）：`WHERE name = ? AND id <> ?` 计数 >0 → `ErrStorageNameConflict`（422）；DB `uniqueIndex` 兜底。同一预检抽函数供 Create 对称套用（Create 撞唯一索引由 500 收口为 422）；
5. S3 且 `Region == ""` → 默认 `us-east-1`（与 Create 一致）；
6. 全量覆盖持久化字段：`Name/Endpoint/Bucket/Region/Prefix/AccessKeyEnv/SecretKeyEnv/UseSSL`，并置 `last_test_at=NULL, last_test_ok=false, last_test_message=''`（用 `Updates(map)` 显式写零值，避开 GORM 零值跳过）；
7. 返回更新后实体（`BackupCount/UsedBytes` 为 `gorm:"-"` 聚合字段，由列表接口重取，此处零值）。

**router（internal/controlplane/router/backup_storage.go）**：

- `Update(c)`：`parseID` → 绑定既有 `createStorageRequest`（字段/必填约束与 Create 完全一致，`name`、`type` required）→ 复用 `storageFromRequest(req)` 组装 → `svc.Update(id, st)`；
- 错误映射：`ErrStorageNotFound` → 404 `NOT_FOUND`；`ErrInvalidStorageType` / `ErrCredentialNotEnvRef` / `ErrStorageTypeImmutable` / `ErrStorageNameConflict` → 422 `BUSINESS_ERROR`（message 带原因）；绑定失败 → 400 `INVALID_REQUEST`；其余 500；
- `RegisterRoutes` 增 `g.PUT("/:id", h.Update)`（沿 admin 路由组，平台管理员）。

**引用与恢复链路影响**（核实）：存储后端被 `backups.storage_id` 引用（`internal/controlplane/model/backup.go`）；定时任务模型不直接引用存储后端（`schedules` 无 storageId，PRD 行中「定时任务引用」的假设不成立，已按源码校准）。恢复远程备份时以**当前配置** + 备份行的 `storageKey` 解析（`ResolveSpec`），故编辑 `endpoint/bucket/prefix` 后旧备份的可恢复性取决于对象是否仍在新指向可达——不加锁，UI 编辑弹窗对「有引用的存储」显示提示条（见 §3.2，风险见 §6）。

### 3.2 前端（apps/control-plane-web）

**api/backupStorages.ts**：

```ts
/** 更新存储后端（FR-338）。body 同创建（type 须与现值一致）。 */
export function useUpdateBackupStorage() // mutationFn: ({ id, ...body }) => api.put(`/backup-storages/${id}`, body)
// onSuccess: invalidate ['backup-storages']
```

（body 类型复用 `CreateBackupStorageBody`。）

**BackupStoragesPage.tsx**：

- 弹窗双模式：新增 `editing: BackupStorage | null` 状态；「编辑」入口把行值受控填入 `form`（`name/type/endpoint/bucket/region/prefix/accessKeyEnv/secretKeyEnv/useSsl`——凭证字段即 `${VAR}` 引用，原样回显）；
- edit 模式下 `type` Combobox 置 `disabled`（后端 422 双保险）；标题与提交按钮文案区分（`backupStorages.edit`「编辑存储后端」/ `common.save`「保存」）；提交走 `useUpdateBackupStorage`，成功 toast + 关窗，失败 toast 后端 message（422 冲突/类型原因可见）；
- 「测试连接」按钮逻辑不变：对当前表单值调 `POST /backup-storages/test`（创建/编辑草稿通用，body 同形）；
- 表格操作列：测试 /「编辑」/ 删除；
- 编辑目标 `backupCount > 0` 时弹窗内显提示条（i18n `backupStorages.editInUseHint`：改 endpoint/bucket/前缀不迁移已有备份对象，可能影响旧备份恢复定位）；
- 弹窗沿用既有 `Dialog + scrollableDialogContentClass + ScrollableDialogBody`（ui-modals 纪律，现结构已合规，仅内容双模式化）。

**devmock（packages/devmock/src/handlers/domains/backup.ts）** 增：

```ts
domainRoute('put', '/backup-storages/:id', …)
// 404 不存在；body.type !== existing.type → 422 BUSINESS_ERROR；
// 名称冲突（其他 id 同名）→ 422；否则更新字段 + lastTestAt=undefined/lastTestOk=false/lastTestMessage=''，返回更新后实体
```

### 3.3 docs/API.md 同步段落草稿

任务拆分内落地，草稿：

```
### PUT /api/v1/backup-storages/:id
- 描述: 编辑远程存储后端（全量替换）。type 不可改（与现值不一致回 422）；凭证字段须为 ${ENV_VAR} 引用；
  名称冲突（排除自身）回 422；成功后清空 lastTest*（旧连通性结论失效）。被备份引用的后端允许编辑（换密钥/endpoint 为合法运维）
- 权限: 平台管理员
- 关联 FR: FR-338
- 请求: 同 POST /api/v1/backup-storages
- 错误: 404 NOT_FOUND；422 BUSINESS_ERROR（类型非法/类型变更/凭证非 ${ENV_VAR}/名称冲突）
（并在 POST /api/v1/backup-storages 条目补「名称冲突 → 422」）
```

## 4. 任务拆分

- [ ] service：`Update` + `ErrStorageTypeImmutable`/`ErrStorageNameConflict` + 名称冲突预检抽函数并对称接入 Create；service 单测（happy / 404 / 改 type 422 / 名称冲突 422（排除自身不误伤）/ 凭证非 env 引用 422 / lastTest* 清空 / 被引用仍可编辑 / S3 region 默认值）
- [ ] router：`Update` handler + `PUT /:id` 注册 + 错误映射；router 单测（`router/backup_storage_test.go` 扩展）
- [ ] 前端 api：`useUpdateBackupStorage`
- [ ] `BackupStoragesPage`：弹窗双模式（受控回显/type 禁用/文案）+ 行「编辑」+ 引用中提示条；DOM 测试（回显、保存刷新、改 type 禁用、草稿测试连接可用、422 错误 toast）
- [ ] devmock backup.ts：PUT 镜像
- [ ] i18n：`backupStorages.edit`、`backupStorages.editInUseHint` 等（zh/en）
- [ ] 文档同步：PRD 状态、API.md（§3.3 草稿落地）、CHANGELOG

## 5. 验收标准

- Go 单测全绿：编辑改名 / 改凭证 env 引用 / 改 endpoint 均落库生效；改 `type` 422；不存在 id 404；名称撞其他后端 422、撞自身名（不改名保存）放行；更新后 `lastTest*` 已清空；被备份引用的后端可编辑；Create 名称冲突由 500 收口为 422。
- vitest（devmock）：编辑弹窗受控回显现值（含 `${VAR}` 凭证引用原样）；保存后列表反映新值；edit 模式 type 不可改；「测试连接」对编辑草稿可用并展示结果；404/422 错误消息可见。
- 真机（需用户确认）：真实 CP 对一个已有备份引用的 S3 后端执行改名 + 换 `secretKeyEnv` + 换 endpoint，保存生效、列表 lastTest 列回到未测试态，随后「测试」按钮按新配置探活成功；尝试把 S3 改成 SFTP 被 422 拒绝。

## 6. 风险 / 待定

- **编辑指向漂移影响旧备份恢复**（本 FR 最大风险）：恢复用「当前配置 + storageKey」解析，改 `endpoint/bucket/prefix` 不迁移数据，旧备份可能定位失败。拍板：不加引用锁（换密钥/修 endpoint 正是主场景），以编辑弹窗提示条承接；**是否对「有引用 + 改动 bucket/prefix」升级为二次确认（DangerConfirm）**→ 待定，倾向仅提示不阻断。
- **保存时不探活**（拍板确认项）：Update 与 Create 对齐，只做静态校验；连通性由显式「测试连接」负责。若要求「保存前强制探活」需改双端交互，倾向不做。
- **lastTest\* 清空语义**：推荐（配置变更后旧结论具误导性），需用户确认；若否决则删去步骤 6 的清空并同步 API.md 措辞。
- **软删除行占用名称的既有瑕疵**：`name` uniqueIndex 含软删行——已删除后端仍占名，预检（默认 scope 排除软删）通过但落库撞索引回 500。属 FR-057 既有现状、本 FR 不修（记录在案，如需修复另立 FR：部分索引或改 uniqueIndex 含 deleted_at）。
- **PUT 全量替换 vs PATCH**（拍板）：PUT 全量——编辑表单受控回显天然携带全量字段，复用 `createStorageRequest`/`storageFromRequest` 零新类型，省 patch 合并歧义；代价是老客户端不可部分更新（无此消费方）。
- **PRD 行措辞校准**：PRD/任务描述中「名称冲突校验（复用）」与「定时任务引用」两处与源码不符——名称冲突预检为**新增**逻辑（现仅 DB 索引兜底），定时任务不引用存储后端；本 spec 按源码事实执行，PRD 行不需改（索引行粒度无此细节）。
