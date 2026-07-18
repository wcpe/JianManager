# ADR-074：Bot 目标实例与执行节点解耦，采用 Control Plane 分布式调度

- **日期**：2026-07-18
- **状态**：accepted（2026-07-18 用户审核通过）
- **关联**：FR-351～357、ADR-006、ADR-016、ADR-066
- **取代/修订**：部分修订 ADR-006 中“50 bots/worker 粒度”的单目标实例单 Worker 隐含归属；不改变 Node.js 子进程与 stdin/stdout IPC 决策

## 上下文

现有 Bot 记录以 InstanceID 归属目标游戏实例，Control Plane 的创建、停止、命令、状态和事件操作均按实例所属节点选择 Worker。Bot Worker 单进程容量固定 50，因此一个目标实例的压测会话无法使用其他 Worker 发压；HTTP count 即使允许 5000，也会在单 Worker 容量处失败。

500+ 塔防压测要求多个发压 Worker 共同连接同一目标服，同时仍需保持目标实例的权限、指标和 ServerProbe 事件归属。不能通过把 Bot Worker 容量常量直接改成 500，也不能新增独立 Bot 微服务破坏三进程模型。

## 决策

1. **目标与执行解耦**
   - `Bot.InstanceID` 继续表示被测目标实例、权限和指标归属。
   - 新增 `Bot.ExecutorNodeID` 表示实际运行 Mineflayer 的 Worker。
   - 未设置 ExecutorNodeID 的普通 Bot 回退实例所属节点，保持兼容。

2. **Control Plane 是全局调度与 desired-state 真源**
   - CP 发现各 Worker Bot 容量、生成分片/批次、保存 assignment 和运行状态。
   - Worker 不访问数据库，只执行 CP 经 gRPC 下发的 assignment。

3. **Worker 是节点容量与 runtime 真源**
   - Worker 通过 Bot Manager 暴露 max/active/connecting/ready/capability/epoch/resource。
   - Bot Worker 继续是 Worker spawn 的 Node.js 子进程，只使用 stdin/stdout JSON 行协议。

4. **分布式扩容，不放大单进程默认容量**
   - 默认仍为 50 Bot/Worker。
   - 500 Bot 由 10 个以上 Worker 分片；容量未来可声明，但必须由节点准入和真机验收证明。

5. **批量、幂等协议**
   - CP↔Worker 使用批量 gRPC，每批最多 50。
   - Worker↔Bot Worker 使用带 requestId/idempotencyKey 的批量 IPC和逐项回执。
   - 写 stdin 不等于 accepted；Node 明确回执 accepted 后才计接受，持续 FleetEvent 才计 connected。

6. **所有 Bot 操作统一按执行节点路由**
   - Create/Delete/Stop/Behavior/Command/Status/Event/Retry 使用 `ExecutorNodeID ?? Instance.NodeID`。
   - `WorkerID` 仅兼容展示，不作路由真源。

7. **能力协商与旧版本兼容**
   - 新版 Worker/bot-worker 声明 fleet feature 和 capacityGeneration。
   - 旧版本继续支持单 Bot/旧会话，但不进入分布式节点池。

8. **故障归责**
   - 目标实例过载、发压节点过载、网络、场景和探针错误分别分类。
   - 单发压节点失败不自动回滚所有已成功批次；运行进入 degraded 并保留证据。

9. **ServerProbe 边界不变**
   - ServerProbe 仍只连接目标实例本机 Worker。
   - 目标 Worker 的可信游戏事件由 CP 关联后再经 gRPC 路由给执行 Bot 的 Worker。

## 理由

- 复用现有 Worker/Bot Worker 部署能力和节点池，不新增服务。
- 目标实例权限与执行资源分离后，一个服可使用整个集群发压。
- 批量协议显著降低 500 Bot 创建时的 gRPC/IPC 开销。
- 保留 50/Worker 的故障隔离和内存边界，避免单 Node 进程承载 500 Mineflayer 的高风险。
- 加性模型与 API 能兼容既有单 Bot和 50 Bot 会话。

## 后果

- Bot 模型、所有 Bot service 路由和 Worker 协议都需要加性改造。
- CP 必须维护容量预检、软预留、批次和状态流。
- 真正的 500 Bot 验收需要 10+ 可用 Worker；没有环境只能标待真机。
- 目标实例 Worker 与执行 Worker 不同时，事件必须经 CP 做跨节点关联和转发。
- CP 仍是单实例调度器；未来 CP HA 需要分布式租约，当前范围外。

## 回滚与兼容

- 不删除 ExecutorNodeID 等加性字段；关闭分布式入口后，普通 Bot继续回退实例节点。
- 旧 Worker 标记 legacy，不参与分片。
- 若分布式运行不可用，既有 CreateBot/DeleteBot/50 Bot V1 路径保持。

## 否决方案

### 单 Bot Worker 直接提高到 500

Mineflayer 世界/实体缓存、pathfinder、事件循环和独立连接内存不可通过改常量证明安全；故障也会一次影响全部 Bot，否决。

### Control Plane 直接 spawn Bot Worker

破坏 CP/Worker 进程边界和节点资源所有权，否决。

### 新建独立 Bot 微服务或消息队列

引入第四进程、部署和运维复杂度，当前规模无必要，否决。

### 复制目标实例到每个发压节点

目标服只有一个，复制实例会改变被测对象和房间语义，否决。
