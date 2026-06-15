# 编码规范 — JianManager

> 本文档原地更新，反映当前团队编码规范。

---

## Go

### 命名
- 包名：小写单词，不带下划线（`process` 不是 `process_manager`）
- 结构体：PascalCase（`ProcessManager`）
- 函数/方法：PascalCase 导出，camelCase 私有
- 接口：以 `-er` 结尾（`Commander`, `Renderer`），单方法接口用方法名（`Start`, `Stop`）
- 常量：PascalCase（`StatusRunning`）
- 文件名：snake_case（`process_manager.go`）

### 目录结构
```
cmd/                    # 入口
internal/               # 内部包，不可被外部导入
  controlplane/         # Control Plane 模块
  worker/               # Worker Node 模块
  shared/               # 跨进程共享
proto/                  # Protobuf 定义
```

### 错误处理
- 使用 `fmt.Errorf("context: %w", err)` 包装错误
- 不要忽略 error 返回值
- 业务错误定义为常量（`var ErrQuotaExceeded = errors.New("quota exceeded")`）

### 日志
- 使用 `log/slog`（Go 1.21+ 标准库）
- 结构化日志：`slog.Info("instance started", "instanceId", id, "nodeId", nodeId)`
- Error 级别记录堆栈信息

### 数据库
- GORM model 字段使用 `gorm:""` tag 控制映射
- 不要在 service 层直接写 SQL，通过 repository 封装
- Migration 使用 GORM AutoMigrate

### 测试
- 文件名：`xxx_test.go`
- 使用 `testing` + `testify/assert`
- 表驱动测试（Table-driven tests）

---

## TypeScript (前端 + Bot Worker)

### 命名
- 组件：PascalCase（`InstanceListPage.tsx`）
- Hook：camelCase，`use` 前缀（`useInstance.ts`）
- 工具函数：camelCase（`formatDate.ts`）
- 常量：UPPER_SNAKE_CASE
- 文件名：组件用 PascalCase，其他用 camelCase 或 kebab-case

### 前端约定
- 所有页面使用 `React.lazy` 懒加载
- 服务端数据统一用 TanStack Query，不用 useEffect + useState
- 客户端 UI 状态用 Zustand
- 样式用 TailwindCSS，不用自定义 CSS，不用内联 style
- 组件从 shadcn/ui 按需拷贝，不安装整个包

### Bot Worker 约定
- IPC 消息类型定义在 `ipc/types.ts`
- 行为继承 `Behavior` 基类
- 所有异步操作用 `async/await`，不用裸 Promise

---

## Git

### Commit Message

遵循 Conventional Commits，详细规范见 `.claude/rules/git-commit.md`。

核心格式：
```
<type>(<scope>): <中文描述>
```

Type：`feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `perf`, `build`, `ci`, `style`
Scope：`control-plane`, `worker`, `bot-worker`, `web`, `proto`, `config`, `api`, `build`, `ci`, `docs`, `sdd`, `deps`

示例：
```
feat(worker): 实现实例生命周期状态机
fix(control-plane): 修复 token 刷新并发竞态
docs: 更新 API.md 新增 bot 接口定义
```

### 分支
- `main` — 稳定版本
- `dev` — 开发分支
- `feat/xxx` — 功能分支
- `fix/xxx` — 修复分支
- `hotfix/xxx` — 紧急修复分支
