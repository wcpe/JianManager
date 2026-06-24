# ADR-027: 业务命令复用插件桥 command/event 帧 + domain.verb 寻址

- **日期**: 2026-06-24
- **状态**: accepted（骨架；实现细节随 FR-115 落地）
- **上下文**: JBIS 业务命令/事件需要一条 CP↔探针的双向通道。ADR-016 的插件桥（反向 WS，通用 JSON 帧：上行 hello/ping/event、下行 welcome/pong/command，`requestId` 往返 `command_result`）已存在并经真机验证。问题：为业务另起通道/协议，还是复用桥？

## 决策

**完全复用既有桥，不新增进程、不新增通信协议；业务与监控/治理在同一桥上按 `domain` 命名空间分流。**

1. **寻址 = `domain.verb`**：治理为内建 `core.*`（kick/ban/...，既有），业务为 `economy.*` / `inventory.*`。Worker 按 `domain` 前缀路由，**语义零改动**——只多一层前缀分发。
2. **帧加性扩展**：
   - 桥 JSON 帧加 `scope=business` 会话区分 + `domain` 字段。
   - gRPC `proto/worker.proto` 的 command/event message 加性新增 `domain` / `dedup_key` 可选字段，workerpb 经 **protoc 重生成**（禁手改 pb.go）。
3. **控制下行**：CP 生成 `domain.verb + requestId + args + 操作者身份` → gRPC → Worker → 桥 command 帧 → BusinessHost 路由到 Provider → 插件执行 → `command_result` 原路返回。
4. **汇聚上行**：插件事件 → Provider → BusinessHost 转标准 `JmEvent`（带 `dedupKey`）→ 桥 event 帧 → Worker → gRPC 流 → CP → 去重落库。

## 理由
- 复用已验证的通道/往返/会话/鉴权/降级，Worker 侧改动最小化（仅按前缀路由）。
- 不违反架构不变量：依赖方向（CP→Worker→探针→插件）、进程边界、数据所有权全不变。
- `dedupKey` 一字段支撑跨节点重试防重与事件至少一次投递去重。

## 后果
- `proto/worker.proto` 加 `domain`/`dedup_key` 可选字段，下游 CP/Worker 透传（FR-115/116）。
- 桥握手区分 `scope=business`，单连接多路复用监控+业务（探针仍单连接单身份，ADR-025）。
- 幂等键从 CP 经桥透传直达插件幂等键（端到端防重，FR-121）。

## 关系
- **ADR-016（治理桥）**：本 ADR 扩展其桥语义（业务 domain 与治理 core.* 同通道分流）。
- **ADR-002（gRPC 节点通信）**：业务命令/事件仍只经 gRPC + 桥，无新协议。
- **设计总纲** §4.2/§4.3（对应其建议 ADR-027）。
