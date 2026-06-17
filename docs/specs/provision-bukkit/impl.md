# 实施计划 — FR-034 搭建 Bukkit 子服

> 关联 FR: FR-034 | 优先级: P1 | 状态: 🔨 in-progress

## 任务拆解

### Phase 1: 核心下载
- [ ] 新增 `internal/worker/provision/core_download.go`。
- [ ] 支持 Paper/Purpur 下载源；Spigot 可先提示需 BuildTools 或后续实现。
- [ ] 新增 `GET /cores` 聚合可用版本。

### Phase 2: 子服搭建
- [ ] 新增 `ProvisionServer` Worker RPC。
- [ ] 自动创建系统分配 workDir。
- [ ] 下载 jar 到工作目录。
- [ ] 写入 eula/server.properties/spigot.yml/paper 配置。
- [ ] 生成结构化 launchSpec。

### Phase 3: Control Plane 创建流程
- [ ] 新增 `POST /instances/provision/bukkit`。
- [ ] 调用资源分配、JDK 校验、Worker provision。
- [ ] 创建 Instance(role=backend) 并注册到 Worker。
- [ ] 可选调用 proxy registration。

### Phase 4: 前端向导
- [ ] 核心类型/版本选择。
- [ ] JDK 与内存参数选择。
- [ ] 代理注册可选项。
- [ ] 创建后跳转实例详情并可一键启动。

## 风险

- 下载源网络失败需要明确错误。
- Paper 配置文件路径随版本变化，需通过 FR-031 schema 渐进适配。
