# API 契约：分发错误摘要

> 状态：已审 spec 的接口细化　·　关联 PRD：FR-357

## GET /api/v1/client-dist/error-summary

- **描述**：按时间窗聚合失败分发事件的错误码 TopN，并返回最近失败样例。
- **鉴权**：JWT，平台管理员。
- **审计**：`client_dist_error_summary.query`。
- **数据源**：`client_dist_events`；失败定义为 `status >= 400 OR err_code != ''`。

### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `channelId` | string | 否 | 频道过滤；省略表示全部频道 |
| `range` | string | 否 | `24h` / `7d` / `30d` / `90d` / `180d`，默认 `7d` |
| `topN` | integer | 否 | 错误码条数，默认 10，上限 50 |
| `sampleLimit` | integer | 否 | 失败样例条数，默认 20，上限 100 |

### 响应 200

```json
{
  "from": "2026-07-14T00:00:00Z",
  "to": "2026-07-21T00:00:00Z",
  "topErrors": [
    { "errCode": "INVALID_CLIENT_KEY", "count": 12 }
  ],
  "samples": [
    {
      "id": 1,
      "time": "2026-07-20T12:00:00Z",
      "channelId": "stable",
      "kind": "manifest",
      "errCode": "INVALID_CLIENT_KEY",
      "errReason": "拉取密钥无效",
      "status": 401,
      "ip": "203.0.113.*",
      "machineId": "abcdef…1234"
    }
  ]
}
```

`ip` 与 `machineId` 由服务端脱敏。`errCode` 为空的 HTTP 失败统一聚合为 `HTTP_<status>`，避免失败流量被遗漏。

### 错误

- `400 INVALID_REQUEST`：`range` 非法。
- `403 FORBIDDEN`：非平台管理员。
- `500 INTERNAL_ERROR`：查询失败。
