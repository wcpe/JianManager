# 功能规格：客户端分发单节点源站安全防护

> 状态：已交付　·　关联 PRD：FR-264　·　关联 ADR：ADR-023 / ADR-044 / ADR-054　·　范围：玩家侧 updater ↔ Control Plane 的单节点源站应用层防护与防护中心管理页。

## 1. 背景与目标

在没有 CDN / 云清洗的部署形态下，客户端分发的 manifest、updater-core 与制品端点会直接暴露在公网。FR-264 的目标是在应用层尽可能降低恶意反复拉取对 Control Plane 的拖垮风险，同时让管理员可以通过独立防护中心查看异常 IP / 玩家 / 客户端画像，并手动处置 IP 临时封禁与降级策略。

## 2. 已批准边界

- 频道只能降速 / 降级保护，不能自动封禁频道。
- IP 可自动或手动临时封禁；防护中心必须支持手动解封。
- 玩家名强制上报，但承认可伪造，仅作为粗略参考和人工排查线索。
- 不做 CDN、云清洗、L3/L4 抗 DDoS、验证码、机器码强封禁、频繁轮换拉取密钥。
- updater-core 可由楔子下载并随时更新，可附带实现 429 / `Retry-After` 退避重试。
- 不改其它在飞前端页面，尤其不改 `ClientPublishPage`；防护中心独立新增页面。
- 不引入新依赖、不改构建脚本。

## 3. 需求

### 3.1 启动安全画像

updater-core 启动早期调用 `POST /api/v1/client-security/hello`，请求头携带 `X-Client-Key`，请求体至少包含：

- `channel`
- `playerName`（必填）
- `machineId`
- `installId`
- `coreVersion` / `wedgeVersion` / `manifestVersion`
- OS / Java / launcher / locale / timezone / memoryTier 等粗粒度环境字段

服务端记录 `client_security_hellos` 明细，并按 `(channelId, machineId, installId)` upsert `client_security_profiles`。玩家名不作为可信身份，只用于分析。

### 3.2 应用层防护

- 在玩家消费端点上叠加 FR-264 安全检查：
  - `GET /api/v1/client-channels/:id/manifest`
  - `GET /api/v1/client-channels/:id/updater-core`
  - `GET /api/v1/client-artifacts/:sha256`
  - `POST /api/v1/client-security/hello`
- IP 临时封禁必须在拉取密钥校验前生效，命中返回 `403 IP_TEMP_BLOCKED` 并带 `Retry-After`。
- key 状态机：`normal / observe / throttled / suspended / revoked`。`suspended` 返回 `CLIENT_KEY_SUSPENDED`，`throttled` 或速率超限返回带 `Retry-After` 的 429。
- 频道保护模式只允许降速 / 降级，不封禁频道；命中保护返回 `429 CHANNEL_PROTECTED` 或对应限流错误。
- 制品授权收紧：拉取密钥只能下载所属频道当前 latest manifest 引用的制品、回滚窗口内历史版本制品、或该频道选中的 updater-core 归档制品；跨频道和未授权 sha 返回 `403 ARTIFACT_NOT_ALLOWED`。
- Range 防滥用：禁止 multi-range；极小 Range 计入风控；下载统计按实际返回字节计。
- updater-core 对 429、`Retry-After`、`CLIENT_KEY_SUSPENDED` 等错误做退避处理。

### 3.3 防护中心

新增独立前端页面 `/client-dist-security`，不复用或改动发布页。页面包含：

1. 安全总览
2. 异常请求分析
3. 客户端画像
4. IP 剖析
5. 玩家名剖析
6. 封禁与降级管理
7. 安全分组
8. 遥测告知与采集配置

## 4. 数据模型

新增模型：

- `ClientSecurityProfile`：客户端画像最新状态。
- `ClientSecurityHello`：启动画像上报明细。
- `ClientSecurityRiskEvent`：风险事件，字段包含 `ruleCode`、`severity`、subject/channel/machine/install/player/IP/key 等上下文。
- `ClientProtectionAction`：保护动作与人工处置，`status=active|expired|canceled`，支持 `auto` 标记与取消时间。
- `ClientSecurityGroup`：安全分组配置。
- `ClientSecurityCounter`：小时桶计数 / 字节配额辅助。

扩展模型：

- `ClientPullKey.security_state / throttle_policy_json / security_note / security_updated_at`
- `ClientChannel.protection_mode / protection_policy_json / protection_updated_at`

## 5. API 契约

### 玩家端点

- `POST /api/v1/client-security/hello`：启动安全画像上报，拉取密钥鉴权，成功 202。

### 管理端点（平台管理员）

- `GET /api/v1/client-dist/security/overview`
- `GET /api/v1/client-dist/security/events`
- `GET /api/v1/client-dist/security/profiles`
- `GET /api/v1/client-dist/security/ip-analysis`
- `GET /api/v1/client-dist/security/player-analysis`
- `GET /api/v1/client-dist/security/actions`
- `POST /api/v1/client-dist/security/ip-blocks`
- `POST /api/v1/client-dist/security/ip-blocks/:id/cancel`
- `POST /api/v1/client-dist/security/keys/:id/state`
- `PUT /api/v1/client-dist/security/channels/:id/protection`
- `DELETE /api/v1/client-dist/security/channels/:id/protection`
- `GET /api/v1/client-dist/security/groups`
- `POST /api/v1/client-dist/security/groups`
- `PUT /api/v1/client-dist/security/groups/:id`
- `DELETE /api/v1/client-dist/security/groups/:id`
- `GET /api/v1/client-dist/security/privacy-notice`

## 6. 验收

- IP 临时封禁先于密钥鉴权生效，解封后消费端点恢复。
- key 暂停 / 限速、频道保护、带宽 / 并发限制返回稳定错误码与 `Retry-After`。
- 跨频道或未授权制品返回 `ARTIFACT_NOT_ALLOWED`。
- 启动画像缺必填字段返回 400，合法请求 202 并落画像表。
- updater-core 生成并持久化 `installId`，上报强制 `playerName + machineId + installId`，并对 `Retry-After` 做退避。
- 防护中心独立可访问并消费真实聚合端点。
- 相关后端、updater-core、前端检查通过；若全量检查被其它在飞页面阻塞，需在交付报告中明确阻塞文件与原因。
