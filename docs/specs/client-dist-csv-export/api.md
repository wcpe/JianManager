# API 契约：客户端分发 CSV 导出

> 状态：已审　·　关联 PRD：FR-361

## GET /api/v1/client-dist/export

- **描述**：按 kind 导出当前筛选窗口的统计汇总 / 分发事件 / 安全日志 CSV。
- **鉴权**：JWT，平台管理员。
- **限流**：每用户 1 次/分钟 → `429`。
- **审计**：`client_dist.export.csv`。

### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `kind` | string | 是 | `stats-summary` \| `dist-events` \| `security-logs` |
| `channelId` | string | 否 | 频道过滤 |
| `range` | string | 否 | `24h`/`7d`/`30d`；缺省 `7d`；>30d 对明细 kind 拒绝 |
| `errCode` | string | 否 | 错误码过滤（事件） |
| `outcome` | string | 否 | 如 `failure` |
| `ip` | string | 否 | IP 过滤 |
| `machineId` | string | 否 | 机器码过滤 |

### 响应 200

- `Content-Type: text/csv; charset=utf-8`
- 可选 `X-Export-Truncated: true`
- Body：UTF-8 BOM + camelCase 表头 + 数据行

### 错误

- `400 INVALID_REQUEST`
- `403 FORBIDDEN`
- `429 RATE_LIMITED`
- `500 INTERNAL_ERROR`
