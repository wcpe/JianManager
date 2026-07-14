# API Spec — FR-338 备份存储配置编辑

> 关联 FR: FR-338（增强 FR-057/152） | 优先级: P2 | 变更类型: 新增 1 个端点 + 既有 Create 错误语义收口

## 概述

新增 `PUT /api/v1/backup-storages/:id`（全量替换式编辑，body 同 Create）；同批把 `POST /api/v1/backup-storages` 的名称冲突从 500 收口为 422（与 PUT 同源预检）。其余存储端点（GET / POST /test / GET /local/stats / GET /:id/stats / POST /:id/test / DELETE /:id）不变。

## PUT /api/v1/backup-storages/:id

- **描述**: 编辑远程存储后端（全量替换）。`type` 不可改；凭证字段须为 `${ENV_VAR}` 引用（不收明文，config-files.md）；名称冲突排除自身；成功后清空 `lastTestAt/lastTestOk/lastTestMessage`（配置已变，旧连通性结论失效）。被备份引用的后端**允许**编辑（换密钥/endpoint 为合法运维场景，不加引用锁）
- **关联 FR**: FR-338
- **权限**: 平台管理员（admin 路由组 `RequireRole(RolePlatformAdmin)`）
- **保存不探活**: 与 Create 对齐仅做静态校验；连通性由 `POST /backup-storages/test`（草稿）或 `POST /backup-storages/:id/test`（已存）显式测试

### 请求

Path: `id`（uint，存储后端 ID）。Body 与 `POST /api/v1/backup-storages` 完全一致（`name`、`type` 必填）：

```json
{
  "name": "s3-primary",
  "type": "s3",
  "endpoint": "minio.internal:9000",
  "bucket": "jm-backups",
  "region": "us-east-1",
  "prefix": "prod/",
  "accessKeyEnv": "${JIANMANAGER_BACKUP_S3_AK}",
  "secretKeyEnv": "${JIANMANAGER_BACKUP_S3_SK}",
  "useSsl": true
}
```

- `type` ∈ `s3 | sftp | webdav`，且**必须等于该后端现值**（改型=删重建）
- `useSsl` 省略时按 true（与 Create 一致）；S3 且 `region` 空时服务端默认 `us-east-1`
- `bucket`/`region` 仅 S3 有意义；`accessKeyEnv`/`secretKeyEnv` 允许空（如匿名 WebDAV），非空必须整串为 `${VAR}` 形式

### 响应（200）

更新后的存储后端实体（字段同 `GET /api/v1/backup-storages` 元素；`lastTest*` 已清空；`backupCount/usedBytes` 为列表聚合字段，此处为 0 值，前端以列表刷新为准）：

```json
{
  "id": 3,
  "name": "s3-primary",
  "type": "s3",
  "endpoint": "minio.internal:9000",
  "bucket": "jm-backups",
  "region": "us-east-1",
  "prefix": "prod/",
  "accessKeyEnv": "${JIANMANAGER_BACKUP_S3_AK}",
  "secretKeyEnv": "${JIANMANAGER_BACKUP_S3_SK}",
  "useSsl": true,
  "lastTestOk": false,
  "lastTestMessage": "",
  "backupCount": 0,
  "usedBytes": 0,
  "createdAt": "2026-05-20T08:00:00Z",
  "updatedAt": "2026-07-15T09:00:00Z"
}
```

### 错误码

| HTTP | error | 业务错误 | 触发 |
|---|---|---|---|
| 400 | `INVALID_REQUEST` | — | body 绑定失败（缺 `name`/`type`）、id 非法 |
| 401 | `UNAUTHORIZED` | — | 未认证 |
| 403 | `FORBIDDEN` | — | 非平台管理员 |
| 404 | `NOT_FOUND` | `ErrStorageNotFound` | 后端不存在 |
| 422 | `BUSINESS_ERROR` | `ErrInvalidStorageType` | `type` 不在枚举 |
| 422 | `BUSINESS_ERROR` | `ErrStorageTypeImmutable` | `type` 与现值不一致 |
| 422 | `BUSINESS_ERROR` | `ErrCredentialNotEnvRef` | 凭证非 `${ENV_VAR}` 引用 |
| 422 | `BUSINESS_ERROR` | `ErrStorageNameConflict` | 名称与其他后端冲突（排除自身） |
| 500 | `INTERNAL_ERROR` | — | 其他 |

422 响应体形如 `{ "error": "BUSINESS_ERROR", "message": "存储后端类型不可修改" }`（message 带具体原因，与 Create 一致）。

## POST /api/v1/backup-storages（错误语义收口，随批）

- **变更**: 名称与既有后端冲突时由 500 `INTERNAL_ERROR`（DB 唯一索引裸撞）收口为 422 `BUSINESS_ERROR`（`ErrStorageNameConflict` 预检，与 PUT 同源）
- **关联 FR**: FR-338（对齐语义）；其余请求/响应/错误不变（FR-057/152）

## TypeScript 类型（可直接生成）

```ts
/** PUT body 与创建同形；type 须与现值一致。 */
export type UpdateBackupStorageBody = CreateBackupStorageBody
export interface UpdateBackupStorageVars extends CreateBackupStorageBody { id: number }
// 响应复用既有 BackupStorage interface（apps/control-plane-web/src/api/backupStorages.ts），无新增字段
```

## 一致性核对（gate-api）

- 通信协议：浏览器 → CP HTTP；保存不触碰 Worker/gRPC（探活仍走既有 test 端点，其中 `/:id/test`→`TestConnection` 经 gRPC 委托 Worker 的路径不变），符合 ARCHITECTURE.md
- 数据模型：`model.BackupStorage` 无表结构变更；写字段 `name/type(不变)/endpoint/bucket/region/prefix/access_key_env/secret_key_env/use_ssl/last_test_*`，与 ARCHITECTURE.md 既有表一致
- devmock 契约镜像：`packages/devmock/src/handlers/domains/backup.ts` 增 PUT（404 / 改 type 422 / 名称冲突 422 / 重置 lastTest*）
