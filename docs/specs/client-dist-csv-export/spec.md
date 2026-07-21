# 功能规格：分发统计与安全日志 CSV 导出

> 状态：草拟　·　关联 PRD：FR-361　·　分支：feature/fr-361-csv-export  
> 增强：FR-095 / FR-249 / FR-264　·　依赖：**FR-356** 列语义、**FR-360** 脱敏；建议 **FR-357/358** 筛选稳定后实现

## 1. 背景与目标

运营需要把当前筛选窗口内的分发统计汇总、请求事件日志、安全日志导出为 CSV，便于离线分析与工单。须：**仅平台管理员**、**列脱敏**、**限流防拖库**。

## 2. 需求（要什么）

### 2.1 范围内

1. **导出类型**（至少一个端点族，query 区分 kind）  
   | kind | 内容 | 列语义来源 |
   |---|---|---|
   | `stats-summary` | 当前窗 KPI 汇总 + 可选按日行 | FR-356 字典字段名 |
   | `dist-events` | 分发请求事件明细 | FR-249/265 事件字段 |
   | `security-logs` | 安全全量日志 | FR-264/358 日志类型 |

2. **筛选**  
   - 与当前页一致：`channelId`、时间窗/`days`/`range`、`errCode`、`outcome`、`ip`、`machineId` 等（实现时与前端 query 对齐）。  
   - 导出**只反映请求参数**，不依赖浏览器 UI 状态。

3. **权限与限流**  
   - JWT 平台管理员；其它 403。  
   - 限流：每用户/每 IP 例如 1 次/分钟、单次最大行数（默认 10_000，超出截断并在文件头注释或尾列标记 truncated）。  
   - 审计：`client_dist_export.csv` 类 action。

4. **脱敏**  
   - 导出列对 `playerName`/`machineId`/`installId` 使用与面板相同的 `privacy-mask` 规则（服务端实现等价逻辑，不导出明文）。  
   - 不导出拉取密钥明文、完整鉴权 header、request body 中的敏感字段。

5. **格式**  
   - `text/csv; charset=utf-8`，BOM 可选（Excel 友好）。  
   - 首行表头英文 camelCase 或中英双列表头二选一，**在实现中写死一种**并在 API 文档声明。  
   - Content-Disposition 附件文件名含 kind + 日期。

### 2.2 不做

- 异步超大导出任务中心（首版同步限流足够）  
- 自定义列配置器  
- 非管理员角色细粒度授权  
- Excel xlsx  

## 3. 设计（怎么做）

### 3.1 API（建议）

```
GET /api/v1/client-dist/export?kind=stats-summary|dist-events|security-logs&...filters
```

- 鉴权：平台管理员  
- 200：CSV 流  
- 400：非法 kind/范围  
- 403：非管理员  
- 429：限流  

### 3.2 实现要点

- 服务层流式写 CSV（避免全表载入内存；分页扫 DB）。  
- 脱敏函数与前端规则单测对齐（共享文档表）。  
- 前端：统计/日志/安全页「导出 CSV」按钮，带当前筛选；禁用态+限流提示。

## 4. 任务拆分

- [ ] 导出服务 + 限流 + 审计 + Go 测试  
- [ ] 三 kind 列定义与脱敏  
- [ ] 前端按钮与 i18n  
- [ ] API/CHANGELOG/PRD  
- [ ] 可选 Playwright 下载断言  

## 5. 验收标准

- [ ] 管理员可导出三类 CSV，列与筛选一致。  
- [ ] 非管理员 403。  
- [ ] 触发限流返回 429。  
- [ ] 导出中 machineId/playerName 已脱敏，无密钥明文。  
- [ ] 超行数截断有明确标记。  
- [ ] 测试红→绿。  

## 6. 风险 / 待定

- 安全日志多源 union 查询成本：须强制时间窗上限（如 ≤30d）。  
- 与 FR-357/358 筛选键不完全一致时，以 API 文档冻结的 filter 集合为准。  
