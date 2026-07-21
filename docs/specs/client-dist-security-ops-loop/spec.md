# 功能规格：安全中心研判处置闭环 + 频道安全摘要

> 状态：草拟　·　关联 PRD：FR-358　·　分支：feature/fr-358-security-ops-loop  
> 增强：FR-096 / FR-264　·　建议依赖 FR-356（安全微标语义）；与 FR-357 可并行　·　脱敏复用 FR-360

## 1. 背景与目标

客户端分发安全页（原防护中心）已有 overview / events / logs / profiles / IP·玩家分析 / 封禁与 key 态 / 分组等能力，但运营仍缺：

- 画像**详情全量字段**与风险**时间线**；
- 事件行**一键处置**（封 IP / 改 key 态 / 频道防护）+ DangerConfirm；
- **频道工作台**安全摘要条 → 深链安全中心；
- 安全日志 detail **结构化**；
- 不可信字段（playerName/machineId 等）**统一角标**。

本 FR 在 **不重写 L7 规则引擎内核 / key 状态机** 前提下完成研判→处置闭环。

## 2. 需求（要什么）

### 2.1 范围内

1. **画像详情**
   - 全量 profile 字段（含 FR-360/安全 hello 环境字段：os/arch/java*/locale/timezone/memoryTier/coreVersion 等）。
   - 风险时间线：recentEvents + 保护动作历史（按时间倒序）。
   - 脱敏：`privacy-mask`；playerName 标「不可信」。

2. **事件一行处置**
   - 对 security event 提供快捷动作：封禁 IP、设置 key 态（observe/throttled/suspended/revoked）、频道防护模式（throttle/concurrency/queue/retry_after 等现有枚举）。
   - 一律 **DangerConfirm**；成功 toast + 列表刷新；失败可诊断错误。
   - 复用既有 mutation API（`clientDistSecurity`），不另起一套状态机。

3. **频道安全摘要条**
   - 频道工作台顶/统计旁：风险级、近窗异常请求数、封禁 IP 数/受限 key 数等摘要。
   - 点击跳转安全中心并带 `channelId`（与 FR-359 query 对齐）。
   - 允许新只读聚合：`GET /client-channels/:id/security-summary` 或 `GET /client-dist/security/summary?channelId=`。

4. **安全日志 detail 结构化**
   - telemetry/runtime/hello/event 等 type 的 detail 分区展示（键值表），非整坨 JSON。
   - FR-360 新字段在 telemetry detail 可见。

5. **不可信角标**
   - playerName / machineId / installId / 客户端自报环境字段统一角标或脚注组件。

### 2.2 不做

- 重写 key 状态机或 L7 规则引擎  
- CDN/多节点边缘防护  
- 告警联动 FR-216  
- CSV 导出 → FR-361  
- 跨页全部 query 互通（预留 channelId）→ FR-359  

## 3. 设计（怎么做）

### 3.1 后端

- 画像详情：已有 `ProfileDetail` 则补字段与 recentEvents 完整性；动作列表可按 subject 过滤。  
- 频道安全摘要：只读聚合 overview 切片 + channel 维度计数（异常/封禁/防护态）。  
- 鉴权：平台管理员；写操作审计（既有 action 名 + i18n）。  

### 3.2 前端

- `ProtectionCenterPage`：详情抽屉/页、事件行动作菜单、日志 detail 结构化。  
- 频道工作台组件：`ChannelSecuritySummaryBar`。  
- 共享：`UntrustedFieldBadge`、`privacy-mask`、DangerConfirm。  

### 3.3 与 FR-356

- 安全微标 `securityBadge` 语义与 FR-356 字典 key 对齐；无数据不显示假绿。

## 4. 任务拆分

- [ ] 频道安全摘要 API + 测试  
- [ ] 画像详情 + 时间线 UI  
- [ ] 事件行一键处置 + DangerConfirm  
- [ ] 日志 detail 结构化 + FR-360 字段  
- [ ] 不可信角标组件  
- [ ] 频道工作台摘要条 + 深链  
- [ ] vitest/dom + mock 浏览器  
- [ ] 文档同步  

## 5. 验收标准

- [ ] 画像详情可见全量环境字段与风险时间线。  
- [ ] 事件行可完成至少：封 IP、改 key 态、设频道防护（有确认、可取消）。  
- [ ] 频道工作台摘要与安全中心同频道数字一致（允许延迟秒级）。  
- [ ] 不可信字段有统一角标；脱敏格式符合 privacy-mask。  
- [ ] 自动化测试红→绿；非管理员写操作 403。  

## 6. 风险 / 待定

- 一键处置误点：必须 DangerConfirm + 可撤销动作优先（解封/恢复 key 路径保持可达）。  
- 摘要 API 与 overview 双源时，以同一服务层函数聚合避免分叉。  
