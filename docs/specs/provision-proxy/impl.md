# 实施计划 — FR-035 搭建代理（BungeeCord/Velocity）

> 关联 FR: FR-035 | 优先级: P1 | 状态: 🔨 in-progress

## 任务拆解

### Phase 1: 代理搭建 RPC
- [ ] 新增 `ProvisionProxy` Worker RPC。
- [ ] 下载 BungeeCord/Waterfall/Velocity jar。
- [ ] 系统分配 workDir 和监听端口。
- [ ] 生成 config.yml 或 velocity.toml。

### Phase 2: 注册关系写入
- [ ] 调用 FR-032 registration service 保存 proxy↔backend 关系。
- [ ] 调用 FR-031 配置引擎写代理 servers/priorities/forced-host。
- [ ] Velocity 场景生成 secret 并下发后端 Paper 配置。
- [ ] 执行跨代理 secret 一致性校验。

### Phase 3: Control Plane API
- [ ] 新增 `POST /instances/provision/proxy`。
- [ ] 创建 Instance(role=proxy) 并注册到 Worker。
- [ ] 返回代理实例与 backend 注册结果。

### Phase 4: 前端向导
- [ ] 代理类型选择。
- [ ] backend 多选与 alias/priority/forcedHost 编辑。
- [ ] Velocity secret 提示与校验展示。

## 风险

- Velocity secret 需与多个后端配置保持一致，必须依赖 FR-031 版本化写入。
- Bungee/Velocity 配置格式不同，不要硬编码到通用 registration service 中。
