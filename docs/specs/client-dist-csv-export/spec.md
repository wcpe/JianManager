# 功能规格：分发统计与安全日志 CSV 导出

> 状态：已审　·　关联 PRD：FR-361　·　分支：feature/fr-361-csv-export  
> 增强：FR-095 / FR-249 / FR-264　·　依赖：**FR-356** 列语义、**FR-360** 脱敏；筛选键与 **FR-357/358/359** 对齐  
> 冻结决策：表头英文 camelCase；UTF-8 BOM；限流每用户 1 次/分钟；单次最大 10_000 行；时间窗上限 30d

## 1. 背景与目标

运营需要把当前筛选窗口内的分发统计汇总、请求事件日志、安全日志导出为 CSV，便于离线分析与工单。须：**仅平台管理员**、**列脱敏**、**限流防拖库**。

## 2. 需求（要什么）

### 2.1 范围内

1. **导出类型**（单一端点，query `kind` 区分）

   | kind | 内容 | 列语义来源 |
   |---|---|---|
   | `stats-summary` | 当前窗 KPI 汇总（可附可选按日行，首版至少一行汇总） | FR-356 字典字段名 |
   | `dist-events` | 分发请求事件明细 | FR-249/265/357 事件字段 |
   | `security-logs` | 安全全量日志 | FR-264/358 日志类型 |

2. **筛选**  
   - 与页面 query 对齐：`channelId`、`range`（或 `days`）、`errCode`、`outcome`、`ip`、`machineId`、`from`/`to`（可选）。  
   - 导出**只反映请求参数**，不依赖浏览器 UI 状态。  
   - `dist-events` / `security-logs` 时间窗**上限 30 天**；超窗 400。

3. **权限与限流**  
   - JWT 平台管理员；其它 403。  
   - 限流：按用户 ID（无则 IP）**1 次/分钟**；超限 **429**。  
   - 单次最大行数 **10_000**；超出截断，响应头 `X-Export-Truncated: true`，CSV 末行或注释标记 `truncated=true`。  
   - 审计 action：`client_dist.export.csv`（kind 写入 detail）。

4. **脱敏**  
   - `playerName` / `machineId` / `installId` 与前端 `privacy-mask` 等价（服务端实现，单测对齐规则）：  
     - playerName：长度 >16 截断 + `…`  
     - machineId/installId：`前6…后4`，过短则 `***`  
   - 不导出拉取密钥明文、完整鉴权 header、request body 敏感字段。

5. **格式**  
   - `Content-Type: text/csv; charset=utf-8`  
   - 首字节 **UTF-8 BOM**（Excel 友好）  
   - 首行表头 **英文 camelCase**（冻结，不写中英双列）  
   - `Content-Disposition: attachment; filename="client-dist-{kind}-{yyyyMMddHHmmss}.csv"`

### 2.2 不做

- 异步超大导出任务中心  
- 自定义列配置器  
- 非管理员细粒度授权  
- Excel xlsx  
- 为 FR-359 伪造 `docs/specs` 施工规格（深链属免 spec，本 FR 只消费筛选键）

## 3. 设计（怎么做）

### 3.1 API

```
GET /api/v1/client-dist/export?kind=stats-summary|dist-events|security-logs&channelId=&range=&errCode=&outcome=&ip=&machineId=
```

| 状态 | 含义 |
|---|---|
| 200 | CSV 流 |
| 400 | 非法 kind / 超窗 / 非法参数 |
| 403 | 非平台管理员 |
| 429 | 导出限流 |
| 500 | 内部错误 |

### 3.2 实现要点

- 服务层分页扫 DB + 流式写 CSV（禁止一次全表载入）。  
- 限流可复用/仿 `middleware.RateLimiter`，key=`export:{userID}`，rate≈1/60s 或独立 60s 冷却表。  
- 脱敏函数放 `internal/controlplane/service`（或 privacy 小包），与前端规则单测对齐。  
- 前端：分发监控（统计/日志）、安全中心（日志）提供「导出 CSV」按钮，带当前筛选；429 toast。  
- devmock 提供三类 kind 假 CSV。

### 3.3 与 FR-359

- 筛选 query 键名与 FR-359 冻结集合一致：`channelId`/`ip`/`machineId`/`errCode`/`version`/`tab`（导出消费其中与 kind 相关的子集）。  
- **本 FR 不负责**跨页深链实现；不新建 deep-link 的 `docs/specs/`。

## 4. 任务拆分

- [ ] 导出服务 + 限流 + 审计 + Go 测试（403/429/截断/脱敏）  
- [ ] 三 kind 列定义与路由注册  
- [ ] 前端导出按钮 + i18n + API helper  
- [ ] devmock export handler  
- [ ] API.md / CHANGELOG / PRD 状态  
- [ ] vitest：按钮带筛选参数；可选下载断言

## 5. 验收标准

- [ ] 管理员可导出三类 CSV，列与筛选一致。  
- [ ] 非管理员 403。  
- [ ] 连续两次导出触发 429。  
- [ ] 导出中 machineId/playerName 已脱敏，无密钥明文。  
- [ ] 超行数截断有 `X-Export-Truncated` 或行内标记。  
- [ ] 测试红→绿。

## 6. 风险 / 待定

- 安全日志多源 union 成本：强制 ≤30d 时间窗。  
- 与页面筛选键漂移时，以本 spec + API.md 冻结集合为准。
