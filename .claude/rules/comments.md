# 注释规范

## 原则

注释解释 **为什么**，不解释 **是什么**。代码本身应该自解释「是什么」。

## Go

### 必须注释
- 所有导出的类型、函数、方法、常量（godoc 格式）
- 非显而易见的业务逻辑（为什么这样处理）
- 有争议的架构取舍（**把取舍理由写出来**，不要只写编号——见 [no-ref-in-code.md](no-ref-in-code.md)）
- 临时方案 / workaround（标注 `// HACK:` 或 `// WORKAROUND:` 并解释原因）

### 禁止注释
- 重复代码已经表达的内容（`// 设置用户名` 在 `user.Name = name` 旁边）
- 被注释掉的代码块（直接删除，git 会保留历史）
- `// TODO` 不得超过 2 周，超期必须处理或转为 FR

### 格式
```go
// ProcessManager 管理单个 Worker Node 上所有实例的生命周期。
// 它通过 IProcessCommand 策略接口支持多种启动方式（daemon/docker/direct）。
// daemon 方式必须经 Wrapper 子进程隔离——否则 Worker 重启会连带杀掉
// 运行中的游戏服（进程同属一个 cgroup / 进程组）。
type ProcessManager struct { ... }
```

> **不写编号**：`// 参见 ADR-003` 要求读者跳去翻 ADR 才知道理由，且改号后静默失效。
> 写「为什么要这样」本身，理由就在原地。见 [no-ref-in-code.md](no-ref-in-code.md)。

## TypeScript

### 必须注释
- React 组件的 props interface（JSDoc 格式）
- 复杂的 TanStack Query 配置（缓存策略、失效逻辑）
- 非显而易见的业务逻辑

### 禁止注释
- 类型已经表达的信息
- 被注释掉的代码块

### 格式
```typescript
/** 实例终端页面，直连 Worker Node WebSocket */
interface InstanceTerminalPageProps {
  /** 实例 UUID */
  instanceId: string
}
```

## Bot Worker (Node.js)

- IPC 消息类型必须在 `ipc/types.ts` 中有 JSDoc 注释
- 行为引擎的 Behavior 子类必须注释其触发条件和行为逻辑
