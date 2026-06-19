# 实施计划 — FR-036 一键复制子服 + 配置修正 + 注册

> 关联 FR: FR-036 | 优先级: P1 | 状态: ✅ done

## 任务拆解

### Phase 1: 预检
- [ ] 校验源实例为 backend 且已停止或允许热复制策略明确。
- [ ] 分配目标 workDir 和端口。
- [ ] 检查名称、目录、端口冲突。
- [ ] 输出 dry-run 预览。

### Phase 2: Worker 复制
- [ ] 新增 `CloneInstance` Worker RPC。
- [ ] 复制源 workDir 到目标 workDir。
- [ ] 应用排除规则，跳过运行态文件。
- [ ] 返回复制文件统计。

### Phase 3: 配置修正
- [ ] 调用 FR-031 配置引擎修改 server.properties 端口与 motd。
- [ ] 可选修改 level-name。
- [ ] 保留 forwarding secret。
- [ ] 每个修改生成配置版本记录。

### Phase 4: 注册代理
- [ ] 对所选 proxy 创建注册关系。
- [ ] 写入代理配置。
- [ ] 返回注册结果和校验问题。

### Phase 5: 前端
- [ ] 新增复制向导。
- [ ] 展示 dry-run 预检。
- [ ] 支持选择代理并配置 alias。

## 产出文件范围

| 文件 | 操作 | 说明 |
|---|---|---|
| `proto/worker.proto` | 修改 | CloneInstance RPC |
| `internal/worker/provision/clone.go` | 新增 | 复制实现 |
| `internal/controlplane/service/clone.go` | 新增 | 编排复制流程 |
| `internal/controlplane/router/clone.go` | 新增 | REST API |
| `web/src/api/clone.ts` | 新增 | 前端 API |
| `web/src/components/CloneInstanceDialog.tsx` | 新增 | 复制向导 |

## 风险

- 复制运行中实例可能产生不一致，MVP 建议要求源实例 STOPPED。
- 配置修正与代理注册必须是可回滚/可审计操作。
