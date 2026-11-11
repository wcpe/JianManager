# ADR-110：实例日志使用 VL 原生尾流，经反向隧道与认证 HTTP 流桥接

- 状态：accepted
- 日期：2026-11-12
- 关联：FR-559；既有日志查询授权、实例范围与单 VL 数据所有权保持不变

## 背景

既有 `LogTail` 虽然声明 server-stream，实际先聚合一轮查询、再逐条发送并结束。浏览器 HTTP 端接收 JSON 并定期重取。把这种重取循环包成 SSE 不能得到端到端流式处理；旧行为同时还被单次尾部查询和兼容客户端使用，不能直接更改。

## 决策

1. 新增兼容 RPC `LogTailLive(LogTailRequest) returns (stream LogEvent)`，保留 `LogTail` 原语义。旧 Worker 对新 RPC 返回 Unimplemented，浏览器可明确显示轮询回退，不把一次有界响应误判为实时连接。
2. Worker 的 native subscriber 打开 VictoriaLogs `/select/logsql/tail`，逐行解码并即时发送。它不调用 `Coordinator.Tail` 或周期性 Search，不为每一天维持一个长租约。实例授权集合在连接内固定，新流和新 UTC 日可以继续出现。
3. 用户 LogsQL filter 与授权 selector 分离：授权实例前缀、时间下界放在 VL `extra_filters`，每条返回记录再次校验 `log_source_id`。只接受有限、规范的 `inst:<正整数>` 集合；不接受空集合、通配符、节点源和任意 pipe。CP 自己的查询视图仍在订阅建立前校验；历史 Worker view/cursor 不传给实时读取。
4. CP 提供 `GET /api/v1/logs/federation/live`。浏览器采用原生 fetch，使用 Authorization Bearer header；不把 JWT 放 URL。事件以 SSE `ready/event/heartbeat/error` 帧发送，既有 JWT 与 `log.read` 中间件继续生效。
5. 每条连接最长 45 秒，或到 access token 到期（取较早者）；每 2 秒刷新当前用户及授权集合，权限变化即取消下游。进程总连接上限 32，每用户上限 2；最多 8 个 Worker。订阅队列最多 64 条，单事件最多 64KiB，单次最多 10000 行 / 8MiB；缓慢消费者停止，不无界积压。HTTP 写入有 3 秒 deadline，取消一路传播到 gRPC 和 VL HTTP body。
6. Native tail 的 10 秒 `start_offset` 只用于重连后的有限 best-effort 回放，不是持久游标；断线超过窗口可能缺行，也可能有重复。浏览器必须说明这一边界，不能承诺 ExactlyOnce、全历史完整或跨流严格全局时序。
7. `ready` 只说明订阅的范围与模式，绝不等同于全部 Worker 支持、历史覆盖完整或流已产生数据。`UNSUPPORTED` 可回退轮询；`FORBIDDEN` 停止；`SLOW_CONSUMER/BUDGET_EXCEEDED` 停止并提示收窄条件；`SESSION_EXPIRED/STREAM_UNAVAILABLE` 可有界退避重连。
8. 节点与平台日志仍为普通文件尾部读取，不进入本 VL 尾流；浏览器暂停回看/原生选区期间不更新所读 DOM，恢复策略由页面订阅层控制。

## 替代方案

- 周期性调用有界 Tail 并封装 SSE：没有消除查询轮询开销，且会把批次重复/丢失伪装成实时流，拒绝。
- 直接修改既有 LogTail 行为：旧 CP 的聚合读取会无限等待，混版不安全，拒绝。
- 浏览器直连 VL/Worker：绕过平台授权与网络边界，拒绝。

## 验证与限制

- 自动化覆盖 native HTTP 数据提前投递、独立 scope、跨实例前缀防泄漏、行/字节预算、取消传播、RPC transport、CP 队列背压、HTTP 权限撤销与 SSE UTF-8/分块解析。
- 使用批准的 VictoriaLogs v1.52.0 二进制，在临时数据根和随机 localhost 端口实测原生 tail：实例前缀隔离、OR 表达式和取消通过。测试不读取或修改现有 VL 数据。
- 不在此声称 Chromium 页面集成、生产部署或长时间容量验收已完成；这些由整体工作台验收执行。
