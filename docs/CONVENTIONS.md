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

### 并发与锁
- 共享锁的临界区不得跨越慢操作：`Poll` / 投递（VL 写）/ 持久化（索引、DB 事务）/ `Flush` / `Close` / 跨进程调用（gRPC、HTTP、子进程 IO）一律在锁外
- 新增或修改锁时，在获取点注释**锁序**（完整链路，如 `cycleMu → registerMu → pendingMu → mu`）与**最坏持有时长**及量化口径；量化持续有排队者的锁不得用 `TryLock`（Go `sync.Mutex` 饥饿模式下锁空闲也返回 false）
- 有固定 RPC 截止时间的短路径（控制面入口、运维动作）与长流程**分锁**，不让短路径排队等长流程跑完

> 依据 2026-10-01 两次生产事故：FR-499（登记被整轮采集独占的 `cycleMu` 拖死，CP 四次下发规格全超时、11 台实例无法启动）、FR-496（状态重生后投影代次名复用，首启过、二启卡死）。写法口径见本节，评审清单见 `.claude/rules/gate-merge.md`「长临界区审计」「部署后复验」。

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

### 版本号

单一真值来源为 `internal/version/version.go` 的 `var Version`，命名遵循 `.claude/rules/versioning.md`（ADR-065）：

- **正式发布版** 裸 `X.Y.Z`：仅出现在打了 tag `vX.Y.Z` 的那个提交上。
- **开发版** `X.Y.Z-dev`：两个 tag 之间的所有开发态（`dev` 分支常态），`X.Y.Z` 为按 SemVer（feat→MINOR / fix→PATCH / 破坏性→MAJOR）推断的**下一个目标版本**；`-dev` 为 SemVer 预发布标识，满足 `上个正式版 < X.Y.Z-dev < X.Y.Z`。
- **候选版**（可选）`X.Y.Z-rc.N`：冻结候选时短暂使用。

发版由 `sdd-release-version` 去 `-dev` → 打 tag `vX.Y.Z` → 立即 bump 到下一个 `-dev`。构建期版本经 `-ldflags` 注入（ADR-036），产物命名 `<component>-<os>-<arch>[.exe]` 不含版本号。禁止 `version.go` 停留在已发布的裸版本号造成漂移。
