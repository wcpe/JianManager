# API Spec — FR-080 Worker 一键安装 / 傻瓜部署

> 关联 FR: FR-080 | 关联 ADR: ADR-020 | 权威定义见 `docs/API.md`（HTTP）与 `proto/worker.proto`（gRPC，本 FR 经 metadata 不改 proto），本文件为 feature 视角汇总。

## HTTP（浏览器 ↔ Control Plane）

### 签发 enrollment token（仅平台管理员）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/nodes/enroll-token` | 签发一次性、限时的节点准入 token，返回明文 + 一键安装命令 |

请求体（全部可选）：

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `nodeName` | string | 空 | 预设节点名；留空则注册时以 Worker 上报名生效 |
| `ttlMinutes` | int | 30 | token 有效期（分钟），1~1440 |

响应（`201`）：

```json
{
  "token": "jmet_xxx",          // 明文，仅此次返回、不可二次读取
  "tokenId": 12,
  "tokenPrefix": "jmet_ab12",   // 列表识别用前缀
  "expiresAt": "2026-06-23T12:30:00Z",
  "nodeName": "",
  "controlPlaneGrpc": "cp-host:9100",   // CP 据请求 Host 推断、可被显式配置覆盖
  "installCommandLinux": "curl -fsSL .../install-worker.sh | sh -s -- --control-plane cp-host:9100 --token jmet_xxx",
  "installCommandWindows": "iwr .../install-worker.ps1 -UseBasicParsing | iex; Install-JianManagerWorker -ControlPlane cp-host:9100 -Token jmet_xxx"
}
```

- **审计**：`node.enroll_token.create`（detail 仅含 tokenId/tokenPrefix/nodeName/expiresAt，**绝不含明文**）。
- token **落库只存 SHA-256 哈希**，明文一次性返回。
- 一键命令中的下载地址在公网 release 端点未架设前为占位/可配；脚本支持 `--binary` 本地兜底。

### 列出 / 吊销 enrollment token（仅平台管理员，便于管理未消费 token）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/nodes/enroll-tokens` | 列出 token（仅元数据：前缀/过期/消费状态/预设名，无明文） |
| DELETE | `/api/v1/nodes/enroll-tokens/:id` | 吊销未消费的 token（标记失效，立即不可用） |

- 错误码：`404 ENROLL_TOKEN_NOT_FOUND`。
- DELETE 审计：`node.enroll_token.revoke`。

## gRPC（Control Plane ↔ Worker Node）

> **不改 proto**：enrollment token 经 gRPC metadata header `enroll-token` 传递（同构 FR-004 心跳 `node-secret` 经 metadata 的既有手法）。

### `Register` 行为扩展（FR-004 既有 RPC）

- Worker 注册时在 metadata 携带 `enroll-token`（首次安装时）。
- CP `Register` 分叉：
  - **新节点**（`name` 在 `nodes` 表未命中）：**必须**带有效 enrollment token（存在 + 未过期 + 未消费 + 未吊销）。校验通过 → 创建节点 + 原子标记 token `used`（记 `used_by_node`）→ 返回 `node_uuid`/`node_secret`。校验失败 → `PermissionDenied`（Worker 据此明确报错退出，不重试）。
  - **老节点**（`name` 命中）：重注册不强制 token（既有节点重启不掉线，ADR-020 §1）。
- 心跳鉴权（`node_secret` 经 metadata）完全不变。

## Worker 侧行为（部署/启动）

- 启动入参优先级：本地身份文件 `<dataRoot>/etc/node-identity.json`（有则复用 `node_uuid`/`node_secret`，走重注册）> enroll token（`--enroll-token` / `JIANMANAGER_ENROLL_TOKEN`，无身份文件时首注册必需）。
- 配置加载：真正加载 `worker.yaml`（CP gRPC 地址 / grpc·ws 端口 / data_dir / 日志），env 仍可覆盖（`JIANMANAGER_*`）。
- 注册成功后把 `node_uuid`/`node_secret` 持久化到 `etc/node-identity.json`（0600），重启复用、不重复消费 token。

## 安装脚本

| 文件 | 平台 | 说明 |
|---|---|---|
| `scripts/install-worker.sh` | Linux/macOS | 探测 os/arch → 下载或用 `--binary` 本地二进制 → 写 `worker.yaml` → 启动注册 → 可选 `--service` 装 systemd |
| `scripts/install-worker.ps1` | Windows | 同上，可选装 Windows service（`New-Service`/`sc.exe`） |

脚本参数（两端对齐）：`--control-plane <grpc-addr>`（必填）、`--token <jmet_...>`（必填）、`--name <node>`（可选）、`--binary <path>`（可选，离线/内网）、`--download-url <url>`（可选）、`--install-dir <dir>`（可选）、`--data-dir <dir>`（可选）、`--ws-port`/`--grpc-port`（可选）、`--service`（可选，装系统服务）。

- 脚本幂等：重复执行覆盖配置、重启服务，不重复消费已用 token（Worker 侧靠身份文件保证）。
- enrollment token **不写入 `worker.yaml`**，仅经环境变量/命令行传给首次启动。
