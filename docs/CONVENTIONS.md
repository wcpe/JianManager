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
apps/                   # 可运行入口（monorepo 外壳，FR-285 自 cmd/ 迁入）
  control-plane/        # Control Plane 二进制入口
  worker/               # Worker Node 二进制入口
internal/               # 内部包，不可被外部导入
  controlplane/         # Control Plane 模块
  worker/               # Worker Node 模块
  platform/             # 跨进程共享的平台能力（数据根、gRPC 消息、HTTP 客户端等）
  version/              # 版本号单一真源（ADR-065）
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

### 日志数据面配置与证据

- Worker 本地 SQLite 只存日志元数据，不存 CP 业务数据或全文查询库；迁移/字段扩展必须与 [索引规格](specs/log-index-sqlite/spec.md) 逐字段往返和旧状态兼容要求一致。
- 持久提交资格、请求结果前缀、真实 ACK、逐字段可见性凭据和恢复责任分开建模；后台交办或内存 durable 标记不能当提交成功。`CLEANED` 活动引用必须先取得有界持久审计回执、再短锁复核消费者/hold 后裁剪；IO 不跨共享锁。
- 日志运行时默认值只由实现的公共默认入口产生，配置层只绑定、校验与兼容回退；时长复用批准版本解析，不能在不同装配层各自解释月份或舍入精度。准确键、默认、非法值与启层条件见 [运行时规格](specs/worker-victorialogs-runtime/spec.md) 和 [成本治理规格](specs/log-cost-governance/spec.md)；本批新增键在实现核对前只属于修复目标。
- **生效时点必须标明**：`worker.yml`/环境变量的启动期键（包括 `log.level`/`log.format`）修改后需重启 Worker，不因使用 `LevelVar` 就声称支持热更新。CP 注册/心跳下发的 `log.acq.*` 仅按其已实现白名单动态应用；namespace 启停/健康恢复不等于任意 YAML 配置热加载。
- 产品搬运年龄、native TTL、复制校验/恢复 SLA 与恢复副本责任是不同量；有限 TTL 即使有缓冲也不是最后副本永久保证。生产删除三闸默认全关；仅隔离构造数据可为边界验证启用删除，不得把测试配置写成生产默认。
- cache、Go 软限制、RSS、磁盘和全局预算分别计量；未知样本保留 unknown，显式停止的进程可记录已知 0。准入必须覆盖实际在途/预留并保留有界 WAL 排空，不能通过杀实例或丢数据满足预算。
- 单物理行/单轮读取界不等于逻辑 Multiline 缓存界；生产 pipeline 的有限默认只从一个公共入口取得，Manager/Worker 不另造值。独立 Normalizer 库零值语义与显式合法限制保持，溢出须以既有部分事件状态承接完整原文和源区间，不靠 eventstore 拒绝超大事件代替上游有界性；见 [归一化规格](specs/worker-log-normalizer/spec.md)。
- 2026-10-22 批次验收尚在进行：功能包测、真实 VL/浏览器、持续性能、`SIGKILL` 与真正断电证据分开；未运行不标通过。完整边界见 [Shared Contracts](specs/worker-log-platform-contract/spec.md)。

### 并发与锁
- 共享锁的临界区不得跨越慢操作：`Poll` / 投递（VL 写）/ 持久化（索引、DB 事务）/ `Flush` / `Close` / 跨进程调用（gRPC、HTTP、子进程 IO）一律在锁外
- 新增或修改锁时，在获取点注释**锁序**（完整链路，如 `cycleMu → registerMu → pendingMu → mu`）与**最坏持有时长**及量化口径；量化持续有排队者的锁不得用 `TryLock`（Go `sync.Mutex` 饥饿模式下锁空闲也返回 false）
- 有固定 RPC 截止时间的短路径（控制面入口、运维动作）与长流程**分锁**，不让短路径排队等长流程跑完

> 依据 2026-10-01 两次生产事故：FR-499（登记被整轮采集独占的 `cycleMu` 拖死，CP 四次下发规格全超时、11 台实例无法启动）、FR-496（状态重生后投影代次名复用，首启过、二启卡死）。写法口径见本节，评审清单见 `.claude/rules/gate-merge.md`「长临界区审计」「部署后复验」。

### 测试
- 文件名：`xxx_test.go`
- 使用 `testing` + `testify/assert`
- 表驱动测试（Table-driven tests）

#### 日志相关包（`internal/worker/logs/**`）的测试**不得并行运行**

**现象**：同一份代码、同一台机器，单独跑 `go test ./internal/worker/logs/ingest` 只有 **8 条**失败，
而与其他包/其他 worktree 同时跑时会变成 **70+ 条**，且失败内容看起来毫无关联
（`TestDurableOutbox*` 成片红、`TestAppendRejected*`、`TestDeliver*` 等）。

**根因**：事件段根不是 `t.TempDir()` 本身，而是

```
/dev/shm/jianmanager-events/<basename(数据根)>      # eventstore.MemoryRoot，见 eventstore/store.go
```

测试用 `t.TempDir()` 的 basename 形如 `TestXxx123/001`——**同名用例在不同运行/不同 worktree 之间
会共用同一个 tmpfs 段根**。两个进程并行跑就会互相读到对方的段，报
`eventstore: corrupt segment: unlisted durable segment` 或「同 event_id 多 hash」这类**假象**。

**规避**：

1. 跑日志包测试**串行**：一次只跑一个 worktree/一个包，不要与压测 harness 同时跑；
2. 需要调或写用例时，**自己创一个唯一根名**：
   `t.Cleanup(func() { _ = os.RemoveAll(eventstore.MemoryRoot(filepath.Base(root))) })`
   并在建 Manager 前先 `RemoveAll` 一次（参见 `cursor_registry_restore_test.go`）；
3. 排查「突然大片红」时**先看 `/dev/shm/jianmanager-events/` 有没有别的进程在写**，再怀疑代码；
4. 给结论前必须用「单包、串行、同一 `-timeout`」的口径与干净 HEAD 对照，否则数字不可比。

> 这个坑直接导致过一次误判：同一分支上有 78 条 vs 8 条两种读数，白费一整轮对照实验。

### 概念收敛与注释前提

> 这四条来自 2026-11-05 日志平台的一次系统性追查：从「日志页整页为空」查到最后，**同一个病根出现了三次**。

#### 为什么这类问题特别难查

1. **单看每一处都是对的**。四处 selector 各自逻辑自洽，两份白名单各自都合语法——错误只在「它们不相等」时才显现。
2. **测试夹具会让分叉隐形**。命名空间与源 id 写成同值，缺陷在测试里永不出现；两份白名单没有跨层用例，从没人对照过。
3. **表现是「静默的空」，不是报错**。用户看到的是「这天没日志」「这个维度没数据」，不会去查代码。

#### 四条纪律

1. **一个概念出现第二遍时，立刻收成一个类型/函数**，而不是「先复制一份，回头再统一」。
   回头通常不会来。反例：「分区对应哪些 VL 行」在 4 处各写一遍（`selectorQuery` / `rangeMatchesEvent` / `MergeableScope` / `disjointScopeTerms`），命名空间被当完整源 id，四处全错。
   正面做法：收成唯一类型 `query.SourceScope` + `MatchesLogSourceID`，LogsQL 侧与内存归因侧共用。
2. **跨层概念必须有跨层用例**。CP 与 Worker 各写一份的东西，必须有测试同时看两边（哪怕只是在测试里硬编码核心集合）。
   反例：「分面维度白名单」两侧各一份（Worker `streamFieldAllowed` / CP `ControlledFacetDimensions`），`source_generation` 只在 CP 侧，前端显示「可选但永远没数据」。
3. **注释里的数值前提要能被验证**。写了「上限是 31d」，就该有一条用例或一次实测支撑，否则它会在下次被读到时误导人。
   反例：多处注释写 `-search.maxQueryTimeRange` 是 31d，并据此把回溯窗口设为 30 天；实际启动参数是 `4344h`(=181d)。窗口设小**丢了 88 个分区**（2026-09-20~10-06），而那 88 天 VL 里确有数据。
   教训：**过期的前提会把正确的实现锁成错误的**。凡注释里出现具体数值前提（上限、默认值、版本号），都该有一条用例把它钉住。
4. **夹具必须用真实形状**。兜底办法：夹具里不要用「恰好相等」的值；两个概念能用同一个值表达时，选它们必然不同的那组。
   反例：命名空间与源 id 写成同值，等于把缺陷从测试里删掉了。
   同型例证（2026-11-07，rollog）：`TestRetention` 一直用默认配置（压缩开着）跑，于是「`DisableCompress` 时保留清理根本不执行」这个缺陷从未被触发——补上的回归必须显式设 `DisableCompress: true` 才能钉住它。**“默认配置下的绿灯”不等于“所有配置下的绿灯”**。

#### 评审时的自查问句

- 这个概念（分区/白名单/上限/状态口径）在仓里还有别的实现吗？搜一遍再改。
- 两侧都有的概念，有没有一条用例同时看两边？
- 注释里的每个具体数值，能不能指出钉住它的用例或实测？
- 我的夹具里，有没有两个本该不同的东西恰好相等？

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

### 组件文件头锚点（FR-496 阶段 3）
`packages/ui` 的组件文件头带锚点注释，说明这个组件的**边界与联动面**——
它回答的是「改这里时还要动哪些地方」，而不是复述实现：

```tsx
/**
 * @file Button：全站按钮唯一出口。统一样式变体、焦点环、按压/悬停反馈与禁用态。
 * @input  lib/focus-ring、lib/interaction-overlay、lib/utils 的 cn、Spinner
 * @output Button、buttonVariants、ButtonProps
 * @sync   改动行为时同步 button.test.tsx；导出变化时同步 index.ts
 * @since  FR-496
 */
```

- `@file` 一句话说明**它是什么、为什么存在**（而不是"导出了什么"）
- `@input` 依赖的兄弟模块与外部库，便于判断改动影响面
- `@output` 对外导出面，改名/删除时据此搜索调用点
- `@sync` **最容易漏的一环**：与之联动、必须同改的文件（测试、barrel、规则文档）
- `@since` 引入的 FR 编号

细节的「为什么」写在具体实现处；锚点只给入口与联动关系，不重复。

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
