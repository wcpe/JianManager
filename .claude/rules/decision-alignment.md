# 决策对齐

> 代码实现必须和 `docs/adr/` 中记录的架构决策一致。

## 规则

1. **ADR 中标记为 `accepted` 的决策**，代码实现必须遵循
2. **ADR 中标记为 `superseded-by` 的决策**，代码必须遵循新的 ADR
3. **新增架构决策时**，必须先创建 ADR，再写代码（不允许「先写代码再补 ADR」）
4. **发现代码和 ADR 不一致时**：
   - 如果代码是对的 → 更新 ADR（追加新 ADR 取代旧的）
   - 如果 ADR 是对的 → 修改代码

## 当前生效的关键决策

| ADR | 决策 | 代码含义 |
|---|---|---|
| ADR-001 | Go 作为后端语言 | 不得引入 Node.js/Python 作为后端服务 |
| ADR-002 | gRPC 节点通信 | 不得用 REST API 做 Control Plane ↔ Worker 通信 |
| ADR-003 | 守护进程 Wrapper | daemon 启动方式必须通过 Wrapper 子进程隔离 |
| ADR-004 | 用户组替代多租户 | 不得在表中添加 tenant_id 字段 |
| ADR-005 | go:embed 前端 | 不得为前端单独配置 nginx |
| ADR-006 | Bot Node.js 子进程 | Bot 功能必须通过 Node.js 子进程 + stdin/stdout IPC |
| ADR-074 | Bot 目标与执行节点解耦 | Control Plane 是跨 Worker 调度与 desired-state 真源；Bot Worker 仍由 Worker spawn，默认单 Worker 50 Bot，不得新增第四类进程 |
| ADR-007 | MC 群组服 M:N 建模 + 系统分配工作目录 | proxy↔backend 用 server_registrations(M:N)；群组为非独占软标签；工作目录系统分配，不由用户输入 |
| ADR-008 | MC 结构化启动 + 托管多 JDK | MC 实例由 jdk+jvm参数+jar 派生启动命令；按节点托管多 JDK 并绑定 |
| ADR-094 | Worker 日志本地数据面与 CP 联邦查询 | Worker 可持久化 Worker-owned 日志数据面；CP 业务库、权限真源、指标时序仍仅 CP 可写。Log RPC 只经 ADR-081 反向隧道，浏览器不得直连 Worker/VL |
| ADR-098 | 日志保留、分层与归档 | 到期默认**搬运 COLD**（禁裸删）；删除三闸（`discard`+`sweep.vl_sweep`+VL `-delete.enable`）默认全关、过滤器只由代码按白名单生成；热 90d 分级（3d/7d/30d/90d 可配）/冷 730d 统一；分层触发=年龄+磁盘水位取先到（磁盘触发 min_age=7d）；gz 只归档文件不解析、**本地 gz 一律不动**；对象存储非第一阶段必需 |
| ADR-099 | 日志成本治理与不变量 R | 抑制不得造洞（`MergePositionRanges(Process(in)) == MergePositionRanges(in)`，汇总事件承接，R2/R2b 为锚点）；采样/降级/预算默认关；不得绕过缺口 default-deny 判据、`VerifiedRuns`、verify fail-loud；投递路径不得回归每批 O(段总行数) 遍历；correlation 不得进 `stream_fields` |
| ADR-096 | MCP 端点在 Streamable HTTP 路径上无状态化 | 不得重新引入协议会话（`Mcp-Session-Id` 与随之而来的超时、并发上限、列表/踢线、`SESSION_GONE`）；授权必须取**每请求重建的 principal**；SSE 兼容路径只保留传输连接登记；MCP 运维视图按 Token 聚合（`agent_call_logs`），不依赖进程内状态 |
| ADR-103 | 业务视图与业务契约退回应用，组件库回归设计系统 | 业务视图只在 `apps/control-plane-web/src/components/views/`、业务 lib 只在 `apps/control-plane-web/src/lib/`；`@jianmanager/ui` 只含设计系统（原语/布局/外壳/12 个 helper），不得再并入业务代码；包与应用不得互相引用（应用→包单向）。由 `ui-package-rollback.test.ts` 守门 |
| ADR-104 | 危险操作角色门禁由 `DangerConfirm` 自行判定 | 不在 `DangerConfirm` 与其调用点之间传 `allowed`（已删除该 prop）——调用点只声明 `scope`，判定在组件内。新增危险操作不得自行注入门禁结果，也不得依赖「外壳会拦」；后端 RBAC 仍是最终防线 |
| ADR-105 | AI 助手内置在 Go Control Plane | 助手逻辑必须在 CP 进程内、纯 Go 实现：**不得新增 Node.js/Python 常驻服务或 stdio 子进程**，不得把模型调用或工具循环下放到浏览器（密钥与策略在服务端）。人类 JWT **不得**进入 `/api/v1/mcp`（该端点仍只认 `jmat_` Agent Token，`AgentOrJWT` 中间件保持无实现）；用户委托另走受限执行器，不构造假 AgentPrincipal。AI 配置面的权限判据是 `UserAccess.IsPlatformAdmin`，**不得**用 `requirePlatformAdmin(c)`（该函数只校验 `node.manage`）。供应商/外部 MCP 出站一律经受保护工厂，不得直接 `http.DefaultClient` |

## 检查时机

- 编码前：阅读相关 ADR，确保理解决策理由
- Code Review 时：检查实现是否偏离 ADR
- 发现矛盾时：创建新 ADR 记录变更，标注旧 ADR 为 `superseded-by`
