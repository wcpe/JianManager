# FR-344 · 实例环境变量管理（双区展示 + 可编辑写入 .env）

> 状态：✅ 已交付@v0.18.0 · 类型：feat（增强 FR-034 搭建/结构化启动、ADR-008）
> 盘问结论：实例详情新增「环境变量」页签——**上区**可增删改自定义启动环境变量、保存写入 `.env`、下次启动注入生效；**下区**展示运行中 JVM 实际完整环境（含继承 PATH/JAVA_HOME，只读）。

## 目标

1. 实例详情新增「环境变量」页签，两区展示。
2. 上区：编辑自定义启动环境变量 → 持久化 → 写入工作目录 `.env` 文件 → 下次启动注入 JVM 进程。
3. 下区：展示运行中 JVM 进程的实际完整环境（继承 + JAVA_HOME/PATH + 自定义），只读；停机/平台受限时提示。

## 设计

### 上区：自定义启动环境变量（编辑 + .env）

- **编辑复用既有能力**：`instance.EnvVars`（JSON map）已是源真值，CP `UpdateInstance` 的 `EnvVars` 字段已支持更新且触发启动规格重同步到 Worker（`instance.go`）；env 经 `CommandSpec.EnvVars` → `composeEnv` 在启动注入进程（`wrapper.go:529`，既有）。**故上区编辑无需新后端**，前端页签调 `useUpdateInstance({ envVars })` 即可。
- **`.env` 文件物化**（新增）：Worker daemon wrapper 启动构造 Java 命令时（`buildJavaCmd`）把自定义 EnvVars 物化为 `<workDir>/.env`（`KEY=VALUE` 行 + 生成头注释），供用户在文件管理器/终端查看。**源真值仍是 `instance.EnvVars`（页签编辑）**，`.env` 为启动时按配置重写的生成物（单向）；写失败只记日志不阻塞启动。

### 下区：运行时实际环境（只读）

- 新增 Worker RPC `GetInstanceEnv(uuid)`：取实例 Java 进程 PID（`GetInstancePID`），gopsutil `proc.Environ()` 读取**进程实际环境**返回 map；实例未运行 / 读取不支持（Windows 等平台受限）→ `available=false` + `note`。
- CP `GET /instances/:id/env` → `{ configured, runtime, runtimeAvailable, note }`（`instance.read` 权限）：`configured`=自定义启动 env（可编辑源，解自 `instance.EnvVars`，恒返回）；`runtime`=运行时进程实际环境（尽力而为，未运行/受限时 `runtimeAvailable=false`）。
- 前端下区消费该端点，只读展示；`available=false` 显提示（未运行 / 平台受限）。

### 前端

- 实例详情 `InstanceConsolePage` 新增 `env` 页签（TAB_KEYS）。
- 上区：KEY/VALUE 行编辑器（增/删/改），保存调 `useUpdateInstance`；提示「保存后写入 .env、下次启动生效」。
- 下区：运行时实际环境只读表（搜索/排序可选），`available=false` 显空态提示。

## 非目标

- 直接编辑 `.env` 文件反向同步回 `instance.EnvVars`（`.env` 为单向生成物；改 env 请用页签）。
- Windows/macOS 运行时进程环境读取（gopsutil `Environ` 平台受限，Linux 可用；受限时下区显提示）。
- 敏感值脱敏（v1 原样展示；后续可加）。

## 验收

- [x] 环境变量页签上区可增删改自定义启动 env，保存持久化
- [x] 启动后工作目录出现 `.env` 文件、含所配 env（`KEY=VALUE`）；进程内该 env 生效（真机）
- [x] 下区展示运行中 JVM 实际完整环境（含继承 PATH/JAVA_HOME）；停机/受限显提示
- [x] Go build/test 绿、前端 tsc/lint/vitest 绿
- [x] **真机验**：配一个自定义 env → 启动 → `.env` 存在且进程内生效 → 下区可见

## 关联

- 复用 FR-034/ADR-008 结构化启动 + `composeEnv` 注入；下区 gopsutil 复用 FR-343/FR-170 进程采样能力。
