# ADR-090: 通用二进制搭建与 Beacon 可选协同

- **日期**: 2026-09-20
- **状态**: accepted
- **关联**: FR-441 / FR-442 / FR-443 / FR-444 · 依赖 Beacon FR-221 / FR-222 · 增强 [ADR-011](011-artifact-repository.md)（制品库）· [ADR-007](007-mc-network-model.md)（不推翻）

## 上下文

JianManager 的 `provision_server` 只覆盖 Minecraft 生态（paper / sponge / velocity / waterfall / bungeecord），版本解析强依赖各厂商官方 API。但平台上实际还运行着**非 MC 的配套服务**——最典型的是 Beacon（Go 编写的区服配置中心，约 30MB 单文件二进制）。

实测代价：搭建 60 台 MC 实例的区服拓扑时，需要人工双写两个系统（JianManager 改实例归属 + Beacon 管理台逐个分派小区），共 60 次分派 + 12 次默认入口，全程手工。根因是两系统零集成，且 Beacon 这类非 MC 服务无法纳入平台的搭建与任务追踪体系。

同时存在一个必须守住的边界：**Beacon 与 JianManager 是两套独立系统，可能单独部署**。集成能力一旦成为硬依赖，单侧部署就不可用。

## 决策

1. **通用二进制搭建（FR-441）**：`provision_server` 新增 `coreType=binary`，跳过 MC 核心的「版本 → 构建 → URL」三段解析，改为由请求直接提供制品来源，支持三类：
   - `asset`：制品库既有 asset（复用 ADR-011 的内容寻址与签名分发）
   - `url`：远程地址，由 **Worker** 直连下载 + 可选 sha256 校验
   - `node_file`：节点本地路径，须通过受管目录校验（与 `instance_import` 同口径）

2. **异步强制**：所有 binary 搭建必须走 `TaskService.RunAsync`（新 `TaskKind=binary_provision`），不得同步执行。理由是二进制体积大、下载可达分钟级，前端需可见进度、终态需站内信。沿用 FR-319 已验证的「同步段只做建实例 + 登记任务，下载在 CP 后台 goroutine」两段式。

3. **启动方式复用 `startCommand`（不引入 `binarySpec`）**：`startCommand` 已是自由命令串且 Worker 经 `sh -c` 执行，天然支持任意二进制；引入新结构会与 `launchSpec` 形成双重真源。既有生产实例本就以 `./beacon-1.1.0-linux-amd64` 运行，验证了该路径。

4. **Beacon 预设为语法糖（FR-442）**：`coreType=beacon` 内部展开为 `binary` + 固定参数（文件名、启动命令、角色 `beacon`）。制品来源优先级「制品库 > GitHub Releases」，因为内网可能无法访问 GitHub，且入库制品已过 sha256 校验。

5. **可选协同，绝非依赖（FR-443 / FR-444）**——本 ADR 的**核心约束**：
   - JianManager 的协同端点为**可选配置**；未配置即整体跳过
   - 推送失败**只写审计与告警，不重试、不回滚、不阻塞**本机操作
   - 拉取在对方不可达时返回明确错误且**不改动本地任何数据**
   - Beacon 侧机器注册通道是**默认关闭**的开关（Beacon FR-222），关闭时其行为与 v1.1.0 完全一致

6. **推送只覆盖拓扑变更，不含启停**：仅实例创建/删除/改名/改归属触发推送。启停是高频操作（批量重启 60 台 = 120 次推送），且 Beacon 的在线状态本就由 agent 心跳自维护，推送只会造成重复真源。

7. **复用既有共享 token 作内部信任基础（Go 侧一致性）**：Beacon 的 `X-Beacon-Token` 已存在，但原注释声明「仅防误连，非安全边界」。开启机器注册时**该 token 必须换为强随机值**，由 Beacon 启动校验强制（默认值与开关同开则拒绝启动）。

## 理由

- **为什么不做专用 Beacon 搭建**：那会为单一程序写专用路径，下一个非 MC 服务（如其他代理、监控 agent）还得再写一遍。通用二进制是「一次提升、长期复用」。
- **为什么推送不重试**：拓扑变更频率低（人工操作量级），人工补做代价可接受；重试队列引入的持久化状态机与冲突处理成本不成比例。
- **为什么拉取不自动**：自动建树会与人工已建的分组冲突——分组可能承载非 Beacon 语义的归类。首次对齐必须人工确认映射规则。
- **为什么用共享 token 而非新 mTLS**：Beacon 已有该通道且中间件已实现比对逻辑，改动最小；安全风险由「强制换强随机值 + 默认关闭开关 + 强审计」三重缓解。
- **为什么必须可选**：两系统可能单独售卖/部署，集成成为依赖会让单侧部署不可用——这是产品边界问题，不是技术偏好。

## 后果

- 新增 `coreType=binary` / `beacon`，扩展 `CoreService` 的解析分支（binary 短路）与 Worker 侧下载能力。
- 新增 `TaskKind=binary_provision`；复用既有 `RunAsync` + `NotificationService`，不新建任务框架。
- JianManager 新增 `beacon.*` 配置段（endpoint / token / push-enabled / pull-enabled），全部可选。
- Beacon 新增 `mcp.allow-machine-register` 开关 + 启动校验（与默认 token 组合拒绝）。
- 新增审计动作：`instance.beacon_push_ok` / `instance.beacon_push_fail`（JM 侧）、`identity.machine_registered`（Beacon 侧）。
- **两个仓库的改动互不阻塞**：JM 侧可先实现 binary 搭建（不依赖 Beacon），Beacon 侧可先实现建树与信任通道（不依赖 JM）。

## 备选

- **为 Beacon 写专用 coreType**：被否——不可复用，每接入一个新非 MC 服务都要重写。
- **推送用重试队列 + 最终一致**：被否——引入持久化状态机，与「拓扑变更低频、人工可补」的实际不匹配。
- **拉取自动建树**：被否——会与人工分组冲突，且首次映射规则需人工确认。
- **用 mTLS 替代共享 token**：被否——Beacon 已有共享 token 通道，mTLS 需双方证书体系改造，性价比不足；已用「强制强随机值 + 默认关闭」缓解。
- **启停也推送**：被否——高频且与 agent 心跳形成重复真源。
