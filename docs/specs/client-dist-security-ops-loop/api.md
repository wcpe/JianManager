# API 契约：频道安全摘要与画像详情

> 状态：已审 spec 的接口细化　·　关联 PRD：FR-358

## GET /api/v1/client-channels/:id/security-summary

- **描述**：频道工作台近窗安全摘要（风险级、异常请求、封禁 IP、受限 key、防护模式）。
- **鉴权**：JWT，平台管理员。
- **路径参数**：`id` = channelId。
- **窗口**：服务端默认近 60 分钟。

### 响应 200

```json
{
  "channelId": "skyblock-s1",
  "riskLevel": "info",
  "abnormalRequests": 0,
  "blockedIpCount": 0,
  "restrictedKeyCount": 0,
  "protectionMode": "",
  "windowMinutes": 60
}
```

### 错误

- `403 FORBIDDEN`：非平台管理员。
- `404 CHANNEL_NOT_FOUND`：频道不存在。
- `500 INTERNAL_ERROR`：查询失败。

## GET /api/v1/client-dist/security/profiles/:id

- **描述**：客户端安全画像详情，含环境字段与风险/保护动作时间线。
- **鉴权**：JWT，平台管理员。

### 响应 200

在列表画像字段基础上增加：

```json
{
  "recentEvents": [],
  "protectionActions": []
}
```

`playerName` / `machineId` / `installId` 等客户端自报字段在管理端展示时脱敏并标「不可信」。

### 错误

- `403 FORBIDDEN`
- `404 PROFILE_NOT_FOUND`
- `500 INTERNAL_ERROR`
