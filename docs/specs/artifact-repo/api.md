# API Spec — FR-045 制品库（内容寻址 + 完整性校验）

> 关联 FR: FR-045 | 优先级: P1 | 状态: 📋 todo | 关联 ADR: ADR-011（依赖 ADR-010 数据根）

## 概述

平台所有二进制资产（核心 jar、插件、图片、视频、媒体 blob…）统一进**类型分区的内容寻址（CAS）**制品库：

- 物理存储：数据根 `var/artifacts/<type>/<sha256 前 2 位>/<sha256>.<ext>`，类型内按 sha256 去重、类型间分目录。
- DB 索引 `assets`：sha256 既是寻址键也是去重键；附 md5、size、content_type、source_url、metadata、storage_state、storage_backend、ref_count、last_used_at。
- 入库即算 sha256+md5，提供期望校验和则比对，不符拒收。
- 引用保护：`ref_count>0` 的资产禁止删除。

资产是**平台级共享资源**，所有接口要求平台管理员（与节点/模板一致的平台管理员收敛）。

## 数据模型 `assets`

| 字段 | 类型 | 说明 |
|---|---|---|
| id | uint PK | |
| type | varchar(32) | `core\|plugin\|image\|video\|archive\|blob`；UNIQUE(type, sha256) |
| name | varchar(255) | 人类可读名称（如 `paper-1.20.4`） |
| version | varchar(128) | 版本标记，可空 |
| filename | varchar(255) | 原始文件名（决定扩展名/下载名） |
| sha256 | char(64) | 内容寻址 + 去重键（小写十六进制）；UNIQUE(type, sha256) |
| md5 | char(32) | 辅助完整性 |
| size | int64 | 字节数 |
| content_type | varchar(128) | MIME |
| source_url | varchar(1024) | 来源地址（下载入库时） |
| metadata | text | 类型相关扩展元数据（JSON 字符串） |
| storage_state | varchar(32) | `hot`(默认) / `archived` / `external` |
| storage_backend | varchar(64) | 默认 `local` |
| ref_count | int | 引用计数，>0 禁止删除 |
| rel_path | varchar(512) | 物理文件相对数据根路径（便携登记） |
| created_at | datetime | |
| last_used_at | datetime | 命中去重/复用时刷新 |

## REST API

### GET /api/v1/assets
- **描述**: 列出资产，可按类型筛选、分页。
- **权限**: 平台管理员。
- **Query**: `?type=core&page=1&pageSize=20`（`type` 非法 → 400 `INVALID_TYPE`）。
- **响应 200**:
```json
{
  "items": [
    { "id": 1, "type": "core", "name": "paper-1.20.4", "version": "435",
      "filename": "paper.jar", "sha256": "<64hex>", "md5": "<32hex>",
      "size": 48234123, "contentType": "application/java-archive",
      "sourceUrl": "", "metadata": "", "storageState": "hot",
      "storageBackend": "local", "refCount": 0,
      "relPath": "var/artifacts/core/ab/<sha256>.jar",
      "createdAt": "datetime", "lastUsedAt": "datetime" }
  ],
  "total": 1, "page": 1, "pageSize": 20
}
```

### GET /api/v1/assets/:id
- **描述**: 资产详情。
- **权限**: 平台管理员。
- **响应 200**: 单个资产对象；**404 `NOT_FOUND`** 不存在。

### POST /api/v1/assets
- **描述**: 入库——multipart 上传 **或** 从本地路径登记。入库即算 sha256+md5；同 `(type, sha256)` 去重复用并刷新 `last_used_at`；提供期望校验和则比对，不符拒收。
- **权限**: 平台管理员。
- **方式 A（multipart）** `multipart/form-data`：`file`(必填)、`type`(必填)、可选 `name`/`version`/`contentType`/`sourceUrl`/`metadata`/`expectedSha256`/`expectedMd5`。
- **方式 B（register-from-path）** `application/json`：
```json
{ "type": "core", "path": "/path/to/paper.jar", "name": "paper-1.20.4",
  "version": "435", "filename": "paper.jar", "expectedSha256": "<64hex>" }
```
- **响应 201**: 新建或复用的资产对象。
- **错误码**:
  - 400 `INVALID_REQUEST`（缺 type 或既无 file 也无 path）
  - 400 `INVALID_TYPE`（类型非法）
  - 422 `CHECKSUM_MISMATCH`（期望校验和不符）
  - 500 `INGEST_FAILED`

### DELETE /api/v1/assets/:id
- **描述**: 删除资产；被引用（`ref_count>0`）时拒绝。
- **权限**: 平台管理员。
- **错误码**: 404 `NOT_FOUND`；409 `ASSET_IN_USE`（附当前引用数）。

## 内部能力（非公开 endpoint）

- `AssetService.IngestFromURL(ctx, url, params)`：下载 → 入库（去重命中则不重复落盘），供 FR-034 建服取核心复用。

## 错误码汇总

| HTTP | error | 场景 |
|---|---|---|
| 400 | INVALID_REQUEST | 缺 type / 既无 file 也无 path |
| 400 | INVALID_TYPE | type 非法 |
| 403 | FORBIDDEN | 非平台管理员 |
| 404 | NOT_FOUND | 资产不存在 |
| 409 | ASSET_IN_USE | ref_count>0 禁止删除 |
| 422 | CHECKSUM_MISMATCH | 期望校验和与实际不符 |
| 500 | INGEST_FAILED / INTERNAL_ERROR | 落盘/DB 失败 |

## 一致性

- 与 `docs/ARCHITECTURE.md` §11.1（数据根 `var/artifacts`）、§14（制品库）、§7（`assets` 表）一致。
- 与 ADR-011（CAS + 类型分区 + 引用保护 + 归档就绪）一致。
