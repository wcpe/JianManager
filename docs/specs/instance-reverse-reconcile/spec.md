# 功能规格：CP↔Worker 实例反向对账

> 状态：已审核（2026-07-23）　·　关联 PRD：FR-326　·　分支：feature/fr-326-instance-reverse-reconcile  
> 增强：FR-310 孤儿评估缺口 (b)　·　相关：ADR-003 守护链路；正向对账（心跳真源置 STOPPED）已有  
> 并行计划：`.tmp/parallel-plan-2026-07-23.md`（与 FR-365/369 第一批并行；ADR 若新增用占位 `XXXX`）

## 1. 背景与目标

**正向对账**已存在：CP 以 Worker 心跳为真源，心跳中无实例则可将 DB 状态置 STOPPED。  
**反向缺失**：Worker 上仍在跑/被接管的实例，若 CP 库已无记录（误删记录、脏恢复、手工清库等），进程会成为**永久无主孤儿**，占端口与资源。

本 FR：Worker 心跳携带**在管实例清单**，CP 发现「Worker 有、CP 无」时下发处置（护栏防误杀）。

## 2. 需求（要什么）

### 2.1 范围内

1. **心跳扩展**  
   - Worker 心跳（或附属 RPC）上报本节点当前在管实例 ID 列表（及可选 PID/状态摘要）。  
   - proto/gRPC 契约向后兼容：老 CP 忽略新字段；老 Worker 不上报则 CP 不启用反向对账。

2. **CP 检测**  
   - 收到清单后与 DB `instances` 比对，算出 **orphanRuntime** = Worker 有且 CP 无记录（或 CP 记录已软删且策略视为无）。  

3. **处置策略（防误杀护栏）**  
   - **宽限期**：首次发现后观察 N 分钟（可配置，默认 10m），期间若 CP 又出现记录则取消。  
   - **人工确认开关**：配置项默认 **只告警不自动杀**（平台日志 + 可选审计）；开启 `auto_dispose=true` 才自动下发停止/注销。  
   - 处置动作：请求 Worker 停止该实例进程树并清理本地运行态元数据（PID/sock 等，对齐 FR-310 语义）。  
   - 面板：节点/实例运维区展示「无主运行时」列表与确认按钮（即使 auto 关闭，管理员可手动确认处置）。

4. **可观测**  
   - 平台日志中文；指标或列表：发现次数、宽限中、已处置、已取消。  
   - 审计：手动/自动处置均记。

### 2.2 不做

- 跨节点迁移无主进程  
- 自动重建 CP 实例记录（仅处置运行态，不凭空 invent 业务实例）  
- 改写正向对账逻辑  

## 3. 设计（怎么做）

### 3.1 协议

- 扩展现有 Heartbeat / NodeStatus 消息：`repeated ManagedInstanceRef { instance_id, optional state }`。  
- 新可选 RPC：`DisposeOrphanRuntime(instance_id)`（若不宜塞进通用 Stop）。

### 3.2 CP

- `OrphanRuntimeTracker`：记录 firstSeen、lastSeen、status(pending|confirmed|disposed|cancelled)。  
- 配置：`instance_reverse_reconcile.grace_period`、`auto_dispose`。  
- HTTP（管理员）：列表 + 确认处置。

### 3.3 Worker

- 心跳填充在管实例；执行 Dispose 时杀进程树 + 清 PID 文件（复用 FR-310/325 能力）。

## 4. 任务拆分

- [x] proto + 兼容生成  
- [x] Worker 心跳填充与 Dispose 实现 + 测试  
- [x] CP Tracker + 配置 + HTTP + 审计  
- [ ] 前端无主列表与确认（后端已闭环，UI 后置）  
- [x] 文档：API、ARCHITECTURE、ADR-079、CHANGELOG、PRD  

## 5. 验收标准

- [x] 模拟「Worker 有进程、CP 无记录」：进入 pending，宽限内出现记录则 cancelled。  
- [x] 宽限后 + auto_dispose：下发处置并 disposed（单测假 Worker；真机 partial）。  
- [x] auto 关闭：仅列表/日志，不自动杀；手动确认后处置成功。  
- [x] 老 Worker/老 CP 互通不崩（nil 清单不启用；Unimplemented 记失败）。  
- [x] 测试红→绿；真机建议：删库记录后进程变无主 → 对账路径（可用户确认，partial）。  

## 6. 风险 / 待定

- 误杀护栏优先级高于「自动干净」——默认不自动杀。  
- 实例 ID 在 Worker 侧丢失/重编号：须以 Worker 本地稳定键 + CP UUID 映射清晰。  
