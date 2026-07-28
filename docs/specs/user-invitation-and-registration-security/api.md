# API 规格：受控用户创建与邮箱邀请

> 状态：开发中 · 关联 PRD：FR-405、FIX-公开注册

## 约定

- 所有 `/users` 邀请管理接口需要平台管理员 JWT。
- 邀请令牌、密码与 SMTP 密码永不出现在列表、审计详情或错误响应中。
- 用户名、密码长度均为 3–64 与 8–128；邮箱使用标准地址校验。

### POST /api/v1/users

- 权限：平台管理员。
- 请求：`{ "username": "string", "password": "string", "role": 0|1|10, "status": 0|1 }`。
- 响应：201 `{ "id", "uuid", "username", "role", "status", "createdAt" }`。
- 错误：400 `INVALID_REQUEST`；403 `FORBIDDEN`；409 `USER_EXISTS`。

### POST /api/v1/users/invitations

- 权限：平台管理员。
- 请求：`{ "email": "user@example.com", "sendEmail": true }`。
- 响应：201 `{ "id", "email", "role": 0, "expiresAt", "invitationUrl", "emailDelivery": "sent"|"not_configured"|"failed" }`。
- `invitationUrl` 仅在本次响应出现；`sendEmail=false` 时不投递邮件。
- 错误：400 `INVALID_REQUEST`；403 `FORBIDDEN`。

### GET /api/v1/users/invitations

- 权限：平台管理员。
- 响应：200 `[{ "id", "email", "role": 0, "expiresAt", "used", "usedAt", "revoked", "createdBy", "emailSentAt", "createdAt" }]`。

### DELETE /api/v1/users/invitations/:id

- 权限：平台管理员。
- 响应：200 `{ "message": "已撤销" }`；已使用邀请不允许撤销。
- 错误：403 `FORBIDDEN`；404 `NOT_FOUND`；409 `INVITATION_ALREADY_USED`。

### POST /api/v1/auth/invitations/accept

- 权限：匿名。
- 请求：`{ "token": "string", "username": "string", "password": "string" }`。
- 响应：201 `{ "id", "username", "createdAt" }`。
- 错误：400 `INVALID_REQUEST`；401 `INVITATION_INVALID`；409 `USER_EXISTS`。失效、过期、已用、撤销和伪造令牌均为相同的 401 响应。

### POST /api/v1/auth/register

- 移除。任何请求返回 404；不得保留兼容创建路径。

### PUT /api/v1/settings

- 新增仅管理员可写系统设置：`platform.public_base_url`（完整 HTTP 或 HTTPS 公共基址，后续绝对链接统一复用）、`invite.smtp.host`、`invite.smtp.port`、`invite.smtp.username`、`invite.smtp.password`（仅 `${ENV_VAR}`）、`invite.smtp.from`。
- GET 设置对 `invite.smtp.password` 仅返回脱敏状态，不返回变量值或密码。
