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

## 检查时机

- 编码前：阅读相关 ADR，确保理解决策理由
- Code Review 时：检查实现是否偏离 ADR
- 发现矛盾时：创建新 ADR 记录变更，标注旧 ADR 为 `superseded-by`
