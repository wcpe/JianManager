# API 规格：ServerProbe 本地上传来源

> 关联 FR：FR-411　·　状态：开发中（修复流式上传）　·　关联 ADR：ADR-085

## 上传本地版本

### `POST /api/v1/artifact-packages/serverprobe/versions/upload`

仅平台管理员可调用。请求为 `multipart/form-data`，且字段顺序固定如下：

1. `version`：必填文本 part，必须完整到达且通过校验；
2. `file`：必填且唯一的文件 part，文件名必须以 `.jar` 结尾，文件内容最多 64 MiB。

`version` 必须在 `file` 之前。服务端逐个读取 multipart part；在获得有效 `version` 前遇到 `file`、重复字段、缺字段或其他无效 part 时，立即返回 `400 INVALID_REQUEST`。前端使用 `FormData` 时也必须按此顺序 append。

### 流式写入与限额

- 服务端不得调用 `Request.ParseMultipartForm`、`FormFile`，也不得将上传文件物化为 multipart 临时文件或完整读入内存。
- `file` part 必须直接传给 CAS 入库路径；CAS 只在读取完整文件且确认其文件字节数不超过 64 MiB 后原子提交。
- 超限时返回 `413 UPLOAD_TOO_LARGE`，删除 CAS 临时内容，不创建 `Asset` 或 `ArtifactVersion`。`Content-Length` 只有在计入 multipart 开销后才可用于早期拒绝，不能取代对 `file` part 的实际计数。
- 服务端计算 SHA-256 和 MD5，忽略客户端提供的任何摘要；相同字节可复用已有 CAS asset。

### 成功响应

返回 `201 Created`：

```json
{
  "id": 13,
  "sourceId": 2,
  "version": "0.1.0",
  "releaseRef": "local-upload",
  "assetId": 88,
  "expectedSha256": "...",
  "cachedAt": "2026-08-26T00:00:00Z"
}
```

版本记录属于 `local-upload` 来源，已缓存且可立即复用既有全局 → Worker → 实例选择与 CP-local Worker 拉取链路；上传不改变任何默认或已有实例。

### 错误

| HTTP | 错误码 | 条件 |
|---|---|---|
| 400 | `INVALID_REQUEST` | 非 multipart、字段缺失或重复、`file` 在有效 `version` 之前、版本号无效、文件名非 `.jar` |
| 403 | `FORBIDDEN` | 当前用户不是平台管理员 |
| 409 | `VERSION_EXISTS` | `local-upload` 来源中已有相同版本号 |
| 413 | `UPLOAD_TOO_LARGE` | `file` 内容超过 64 MiB |

本地来源不支持同步；同步接口仅适用于 `github-release` 来源。

## 验收约束

- 路由测试必须覆盖平台管理员成功、非管理员拒绝、缺少 `version`、`file` 先于 `version`、非 JAR、真实超限和同来源重复版本。
- 服务测试必须证明上传读取路径未使用 `FormFile` 或 `ParseMultipartForm`，且超限不会遗留 asset/version。
- 页面测试必须证明来源标签可区分 GitHub Releases（线上拉取）与本地上传，且本地来源没有同步操作。
