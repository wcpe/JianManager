# 实施计划 — FR-033 JDK 与运行时管理

> 关联 FR: FR-033 | 优先级: P0 | 状态: 🔨 in-progress

## 背景

实例启动链路当前以自由文本 `startCommand` 为核心。gRPC 已有 `env_vars` 字段，Worker process 层也有 `EnvVars`，但 Control Plane 没有下发 envVars，Worker direct/daemon 启动也没有系统环境合成、`JAVA_HOME` 或 `PATH` 注入。

## 任务拆解

### Phase 1: 模型与 API
- [ ] 新增 `model.NodeJDK`：节点、vendor、majorVersion、version、arch、path、managed。
- [ ] 扩展 `model.Instance`：`JDKID`、`JavaMajorVersion`、`LaunchSpec`。
- [ ] AutoMigrate 新模型/字段。
- [ ] 新增 `service/jdk.go` 与 `router/jdk.go`。
- [ ] 删除 JDK 时检查实例占用并返回 409。

### Phase 2: Worker JDK 管理
- [ ] 新增 `internal/worker/jdk` 包：registry、detect、download/install、remove。
- [ ] 扩展 `proto/worker.proto`：`ListJDKs/InstallJDK/RemoveJDK`。
- [ ] 新增 Worker gRPC handler。

### Phase 3: 启动环境注入
- [ ] 扩展 `process.CommandSpec`：`JavaHome`、`JDKBinPath`、`LaunchSpec`。
- [ ] CP `registerOnWorker` 下发实例 `EnvVars`、JDK 路径或 JavaHome。
- [ ] direct/daemon 启动均使用系统环境 + JDK 环境 + 实例环境合成。
- [ ] 保证 Windows 使用 `Path` / 非 Windows 使用 `PATH` 时不丢失原环境。

### Phase 4: 前端
- [ ] 新增 `web/src/api/jdks.ts`。
- [ ] 实例创建对话框按节点加载 JDK 列表。
- [ ] 支持选择具体 JDK 或 Java 大版本，缺失时提示安装。
- [ ] 实例详情配置页展示运行时信息。

### Phase 5: 测试
- [ ] JDK 占用校验单元测试。
- [ ] env 合成单元测试：保留系统环境、注入 JAVA_HOME/PATH、实例 env 覆盖。
- [ ] direct 与 daemon 启动命令测试。

## 产出文件范围

| 文件 | 操作 | 说明 |
|---|---|---|
| `internal/controlplane/model/node_jdk.go` | 新增 | JDK 注册表 |
| `internal/controlplane/model/instance.go` | 修改 | JDK/launchSpec 字段 |
| `internal/controlplane/service/jdk.go` | 新增 | JDK 业务逻辑 |
| `internal/controlplane/router/jdk.go` | 新增 | REST API |
| `proto/worker.proto` | 修改 | JDK RPC 和实例字段 |
| `internal/worker/jdk/*` | 新增 | Worker JDK 管理 |
| `internal/worker/process/*` | 修改 | 环境注入 |
| `web/src/api/jdks.ts` | 新增 | 前端 API |
| `web/src/components/CreateInstanceDialog.tsx` | 修改 | JDK 选择 |

## 风险与约束

- 不要一次性移除 `startCommand`；保留 generic 兼容路径。
- direct 与 daemon 必须同时支持 JDK 注入。
- 环境变量必须合并宿主环境，不能只设置自定义 env。
- FR-034/035 依赖结构化启动，应在本 FR 稳定后再接入。
