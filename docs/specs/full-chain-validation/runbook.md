# FR-043 全链路运维打通 — 真机验收手册

> 关联 FR: FR-043 | 优先级: P0 | 状态: 📋 todo（待真机逐条验收并由用户确认）
>
> 本手册把 FR-043 的 7 条验收标准映射为可复现的操作步骤、通过判据与失败定位（满足验收项 6「可复现」、7「可诊断」）。
> 需要一台能跑 Java 的机器作为 Worker 节点，并能访问 `api.papermc.io` 下载 Paper 核心。

## 自动化已覆盖（无需真机）

- 核心解析联网路径：`go test -tags e2e -run Live -v ./internal/controlplane/service/` —— 列版本 → 解析最新构建 → 下载地址 HEAD 可达（已绿）。
- 全后端 `go build ./...` / `go vet ./...` / `go test ./...` 绿；前端 `tsc -b` + `vite build` + `vitest` 绿。
- 终端 WS 地址非硬编码：`service/terminal.go` 按浏览器 `c.Request.Host` 构造 `ws://<host>/ws/terminal`（闭合 FR-019 的 `ws://localhost`）。

## 准备：起 CP + Worker

```bash
go build -o bin/control-plane ./cmd/control-plane
go build -o bin/worker        ./cmd/worker

./bin/control-plane            # 默认 :8080（HTTP）/ :9100（gRPC），数据根 ./data
# 浏览器开 http://<host>:8080，按 FR-017 引导创建首个平台管理员
# 配置 worker.yaml 指向 CP 的 gRPC 地址与注册 token，再：
./bin/worker
```

## 逐条验收

### 1. 节点在线 + 实时指标
- 操作：Worker 启动后，进前端「节点」页。
- 通过判据：节点状态在线；CPU / 内存 / 磁盘指标实时刷新。
- 失败定位：
  - 不在线 → 核对 worker.yaml 的 CP 地址 / 注册 token；CP 日志看 `Register` RPC；放行 gRPC 9100。
  - 无指标 → 心跳 `Heartbeat` 上报异常，看 Worker 日志。

### 2. 一键建服 + 启动 → RUNNING
- 操作：节点页给该节点装一个 JDK（JDK 面板下载 Zulu/Corretto 便携版；Paper 1.21.x 需 Java 21）→ 实例页「⚡ 一键搭建」→ 选节点 / 版本（如 1.21.1）/ 内存 / JDK（构建号留空取最新）→ 提交 → 列表出现实例（STOPPED）→ 点启动。
- 通过判据：核心下载落 `data/var/servers/<slug-shortid>/server.jar`；状态 STARTING → RUNNING；终端可见 `Done! For help, type "help"`。
- 失败定位：
  - 建服返回 502 → 看 `message`：核心下载失败（网络 / sha256 不符）或 `WriteConfig` 失败；实例可能已建（去详情重试或删除重来）。
  - 启动即 CRASHED → 终端日志：JDK 与 MC 版本不匹配 / EULA / 端口占用（端口系统自动分配，冲突看 `ports.go`）。
  - 向导预览行会显示「将下载的文件名 + build#」，可据此确认解析正确。

### 3. 终端交互（闭合 FR-019 生产连通性）
- 操作：实例详情 / 控制台「终端」页 → 输入 `list` 回车。
- 通过判据：返回在线玩家数等输出；终端 `wsUrl` 由 CP 按浏览器访问 Host 构造，**LAN / 远程浏览器可连**，不再是 `ws://localhost`。
- 失败定位：
  - 连不上且 URL 仍为 `ws://localhost` → 非本版本行为（本版本 `terminal.go` 用 `c.Request.Host`，确认运行的是最新二进制）。
  - token 失败 → token 有效期 30s，注意时钟偏移与 `permission`（read/write）。
  - **HTTPS / 反代部署注意**：当前固定 `ws://`，若 CP 经 HTTPS 对外，浏览器会因混合内容拦截，需 `wss://`（见文末「已知限制」）。

### 4. Bot 进服
- 操作：`/bots` 页创建 Bot，目标指向该 RUNNING 实例（server = 实例监听地址，port = 系统分配的 `server_port`）→ 启动 Bot。
- 通过判据：服务端 `list` 在线数 +1 并可见该 Bot 名；前端 Bot 状态变 `connected`。
- 失败定位：
  - 连不上 → 建服默认 `online-mode=false`（离线可进）；核对 server/port 指向实例 `server_port`；节点内网可达性。
  - 状态不变 `connected` → bot-worker 子进程是否被 Worker spawn；看 IPC（stdin/stdout JSON）日志。

### 5. 运维闭环
- 操作：终端 `say hello` 验证服务端广播；切换 Bot 行为；再停止实例。
- 通过判据：停止实例后 Bot 随之断开，状态正确回落 `disconnected`。
- 失败定位：停止后 Bot 仍 `connected` → 实例停止事件未通知 Bot / Bot 重连逻辑未感知目标下线。

### 6. 可复现
- 文档化步骤即本手册；可进一步包成脚本（起 CP/Worker + 调 `POST /instances/provision/bukkit` + 轮询状态）。
- 联网核心解析已由 `core_live_test.go`（`-tags e2e`）自动化覆盖；E2E 扩展到「终端交互 + Bot 进服」属 FR-028 延伸。

### 7. 可诊断
- 每 hop 失败定位见各项「失败定位」。关键日志：CP gRPC（注册/下发）、Worker（register / heartbeat / process）、bot-worker IPC。

## 已知限制 / 待办

- **终端 WS 在 HTTPS / 反代部署下需 `wss://`**：当前 `service/terminal.go` 固定 `ws://`。生产经 TLS 对外时应据 `c.Request.TLS` 或 `X-Forwarded-Proto` 选择 `ws`/`wss`。LAN / 本机 HTTP 验收不受影响；确认存在 HTTPS 部署需求后再做此小修。
- **核心走制品库命中**（FR-045 消费侧）：当前 Worker 直接 HTTP 下载核心；命中制品库免重复下载属后续增量。
