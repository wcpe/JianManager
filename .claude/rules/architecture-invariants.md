# 架构不变量

> 任何代码变更都不得违反以下架构约束。违反即拒绝合并。

## 三进程模型

- Control Plane 是唯一面向浏览器的 HTTP 入口（另有面向玩家客户端 updater 的公网分发端点，见「通信协议」；二者鉴权与暴露面隔离）
- Worker Node 不直接暴露 HTTP API 给浏览器（仅暴露 WS 终端端口）
- Bot Worker 是 Node.js 子进程，由 Worker Node spawn，不由 Control Plane 直接管理

## 进程边界

- Control Plane 不得直接操作游戏服进程，必须通过 gRPC 委托给 Worker Node
- Worker Node 不得直接访问数据库，所有持久化通过 Control Plane API 或 gRPC
- Bot Worker 不得直接访问数据库或 gRPC，仅通过 stdin/stdout IPC 和 Worker Node 通信

## 通信协议

- Control Plane ↔ Worker Node：gRPC（唯一允许的 RPC 协议）
- 浏览器 ↔ Worker Node：WebSocket（仅终端/日志流，需一次性 token 鉴权）
- 监控探针 ServerProbe ↔ Worker Node：**反向 WebSocket**（插件桥 `/ws/plugin-bridge`，探针主动连入本机 Worker，需实例级 token 鉴权 scope=plugin-bridge；探针不直连 CP/DB/gRPC，事件/指令经 Worker 中转。载体=ServerProbe 探针，见 ADR-016，取代 ADR-014 的「探针只读+RCON 治理」、复活 ADR-012 的 WS 通道）
- Worker Node ↔ Bot Worker：stdin/stdout JSON 行协议
- 守护进程 ↔ Worker Node：Unix Socket 二进制帧协议
- 玩家客户端 updater ↔ Control Plane：**HTTP 公网分发端点**（客户端 OTA，FR-087）——仅**消费类**（`GET .../manifest`、`GET /client-artifacts/:sha256`）经**拉取密钥**（`X-Client-Key`）鉴权；**发布端点**（`POST .../files`、`POST .../versions`）走 JWT 平台管理员。拉取密钥半公开（随整包分发必泄露），仅鉴权路由+吊销、**不作内容可信依据**；内容可信靠 manifest 的 **Ed25519 签名** + 单调 version 防降级（ADR-022），L7 防护见 ADR-023

## 数据所有权

- 数据库（SQLite/MySQL）仅 Control Plane 可读写
- 本地实例配置文件仅 Worker Node 可读写
- Bot 配置仅 Bot Worker 可读写

## 前端嵌入

- 前端通过 `go:embed` 嵌入 Control Plane 二进制
- 前端不得直接连接 Worker Node 的 gRPC 端口
- 前端连接 Worker Node WS 端口必须携带 Control Plane 签发的一次性 token

## 依赖方向

```
Control Plane → (gRPC) → Worker Node → (exec + IPC) → Bot Worker
浏览器 → (HTTP/WS) → Control Plane
浏览器 → (WS, 需 token) → Worker Node
```

反向依赖（Worker → Control Plane 的 gRPC 回调除外）不得存在。
