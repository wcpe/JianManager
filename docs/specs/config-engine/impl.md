# 实施计划 — FR-031 配置文件管理引擎

> 关联 FR: FR-031 | 优先级: P0 | 状态: 🔨 in-progress

## 背景

现有系统已有通用文件管理 API 和 Worker gRPC 文件读写能力，但配置管理仍只是裸文件 CRUD：没有配置专用 RPC、版本表、schema 表单、diff/rollback 或跨实例一致性校验。

## 任务拆解

### Phase 1: 数据模型与协议
- [ ] 新增 `model.InstanceConfigVersion`：`instance_id/file_path/content_hash/content/message/author_id/created_at`。
- [ ] 在 `database.AutoMigrate` 注册版本模型。
- [ ] 扩展 `proto/worker.proto`：`ListConfigFiles/ReadConfig/WriteConfig/ValidateConfig`。
- [ ] 生成 `proto/workerpb` 代码。

### Phase 2: Worker 配置引擎
- [ ] 新增 `internal/worker/config` 包：格式识别、路径白名单、properties 行级 round-trip。
- [ ] 新增 `internal/worker/grpc/config_ops.go` 实现配置 RPC。
- [ ] 对 yaml/toml/json 先做语法校验 + 原文保存，避免破坏注释。
- [ ] 输出统一 `ConfigDocument` / `ConfigField` / `ValidationIssue`。

### Phase 3: Control Plane 服务与路由
- [ ] 新增 `internal/controlplane/service/config.go`：调用 Worker、写版本、diff、rollback。
- [ ] 新增 `internal/controlplane/router/config.go` 并挂载到 `router.go`。
- [ ] 接入现有 `AuthzService.CanAccessInstance` 做资源级隔离。
- [ ] 复用 ClientPool 获取目标 Worker。

### Phase 4: 前端配置 Tab
- [ ] 新增 `web/src/api/configs.ts`。
- [ ] 将实例详情 `config` Tab 拆为独立 ConfigEditor。
- [ ] 实现文件列表、原始文本编辑、保存、版本列表、回滚。
- [ ] 后续再逐步增加 schema 表单模式和 diff UI。

### Phase 5: 测试与验证
- [ ] properties round-trip 单元测试：注释、空行、顺序不丢失。
- [ ] config service 版本记录测试：保存/列表/回滚。
- [ ] Worker 路径校验测试。
- [ ] `go test ./internal/controlplane/... ./internal/worker/...`。

## 产出文件范围

| 文件 | 操作 | 说明 |
|---|---|---|
| `proto/worker.proto` | 修改 | 新增配置 RPC 与消息 |
| `internal/controlplane/model/config_version.go` | 新增 | 配置版本模型 |
| `internal/controlplane/service/config.go` | 新增 | 配置服务 |
| `internal/controlplane/router/config.go` | 新增 | REST 路由 |
| `internal/worker/config/*` | 新增 | 配置解析/校验引擎 |
| `internal/worker/grpc/config_ops.go` | 新增 | Worker 配置 RPC |
| `web/src/api/configs.ts` | 新增 | 前端配置 API |
| `web/src/components/config-editor/*` | 新增 | 配置编辑 UI |
| `web/src/pages/InstanceDetailPage.tsx` | 修改 | 接入配置 Tab |

## 风险与约束

- 不要把 FR-031 简化为 `/files/write` 的别名；保存配置必须产生版本记录。
- round-trip 解析优先保障“不破坏原文”，schema 表单可渐进增强。
- Worker 只负责实例 workDir 内的真实文件读写，版本事实源在 Control Plane 数据库。
- 与 FR-032/034/035/036 共享校验规则，接口需保持稳定。
