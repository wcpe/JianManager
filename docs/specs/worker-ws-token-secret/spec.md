# 功能规格：CP↔Worker 专用 WS 令牌密钥自动下发 + 终端 401 诊断兜底

> 状态：草拟　·　关联 PRD：FR-275（P0）/ FR-276（P1，依赖 FR-275）　·　ADR：[ADR-061](../../adr/061-worker-ws-token-secret.md)（修订 ADR-020）　·　分支：dev

## 1. 背景与目标

真机验收缺口（终端 WS 401）：CP 用 `jwt.secret` 签终端/插件桥令牌，Worker 用 `cfg.JWTSecret` 校验，但 enroll/一键安装从不下发该密钥 → 生产 CP 设自定义 secret 后，一键装的 Worker 终端 401、探针插件桥监控一并失效，前端只显示「连接已断开」无诊断。同时 CP 的 `jwt.secret` 是签用户会话的主密钥，不可原样下发。

目标：**专用 WS 令牌密钥**（与用户会话密钥隔离）经 gRPC 注册响应/心跳自动下发并持久化，一键安装的 Worker 开箱终端+监控可用（FR-275）；密钥不一致时给出明确诊断而非裸断连（FR-276）。决策与安全论证见 ADR-061，本文不重复。

## 2. 需求（要什么）

### FR-275
- CP 侧专用 WS 令牌密钥三轨解析：显式配置 `jwt.ws_secret` > 生产态 autogen 持久化 `<dataRoot>/etc/ws-token-secret.key` > dev 回退 `dev-secret-change-me`。
- CP 终端 token、插件桥 token 改用该密钥签发/校验；用户会话签发校验（`AuthService`、路由 JWT 中间件）**不动**。
- 密钥经 `RegisterResponse`（首注册+重注册）下发、`HeartbeatResponse` 每拍携带；Worker 持久化到 `etc/node-identity.json`（0600）并热应用到终端+插件桥校验。
- 向后兼容：旧 CP 响应无字段 → Worker 回退本地 `jwt_secret` 配置/默认，行为与现状一致。

### FR-276
- CP 终端代理连 Worker 握手被 401/403 拒绝时，向浏览器回结构化错误（区别于网络不可达类失败），前端显示「终端令牌被 Worker 拒绝，疑似该节点 WS 密钥与平台不一致」。
- DEPLOY 标注密钥一致性要求、FR-275 自动下发机制、存量/手动部署的核对修复法（仓库无 OPERATIONS.md，运维排查归 `docs/DEPLOY.md`）。

### 不做（范围外）
- 每节点独立 WS 密钥（ADR-061 替代方案，留未来）。
- CP↔Worker gRPC 传输加固（mTLS，ADR-020 既有取舍）。
- 面板展示/轮换 WS 密钥的 UI（无此需求；轮换=改配置或删密钥文件后重启）。
- 强制所有注册带凭据的彻底收口（ADR-020 遗留，另立 FR）。

## 3. 设计（怎么做）

### 3.1 CP 侧密钥解析（新 `internal/controlplane/service/ws_token_secret.go`）

镜像 FR-263 `ResolveKeyEncryptor`（[client_key_crypto.go](../../../internal/controlplane/service/client_key_crypto.go)）：

```go
// ResolveWSTokenSecret(explicit string, devMode bool, keyFilePath string) (secret, source string, err error)
//   explicit 非空          → 用之，source=env（不生成不持久化；过渡逃生口）
//   devMode && 未配        → "dev-secret-change-me"，source=dev（与现状默认一致，保 dev 连续性）
//   生产未配               → 读 keyFilePath；无则生成 32 字节随机 → base64 串 → 0600 原子写，source=generated
//   生成/读取失败          → err（装配层 fail-fast，见 ADR-061 决策 2；绝不回退 jwt.secret）
```

装配（`cmd/control-plane/main.go`）：`dataroot` 已就位（main.go:60），解析一次得 `wsTokenSecret`，替换三个签发/校验点的 `cfg.JWT.Secret` 实参：
- main.go:88 `NewTerminalService(db, wsTokenSecret, ...)`
- main.go:222 `NewPluginBridgeService(wsTokenSecret)`（FR-068 探针在线更新重发 token 经此服务自动随切）
- main.go:413 `NewTerminalProxy(wsTokenSecret, terminalSvc)`
- main.go:66 `NewAuthService(db, cfg.JWT)` 与 main.go:410 `router.Setup(..., cfg.JWT.Secret)` **保持不动**（用户会话）。
- `source=generated` 首次生成打 `slog.Info`（提示文件路径勿删，同 ADR-052 决策 4）。

配置：`config.JWTConfig` 加 `WSSecret string mapstructure:"ws_secret"`，默认 `""`；env `JIANMANAGER_JWT_WS_SECRET` 经既有 replacer 自动生效。设置页 `settings.go` 只读项列表加 `jwt.ws_secret`（掩码显示，同 `jwt.secret` 处理）。

### 3.2 gRPC 下发（proto + CP handler）

proto（`proto/worker.proto`，向后兼容加字段）：
```proto
message RegisterResponse {
  string node_uuid = 1;
  string node_secret = 2;
  string ws_token_secret = 3;  // CP↔Worker WS 令牌密钥（FR-275，见 ADR-061）；空=旧 CP，Worker 回退本地配置
}
message HeartbeatResponse {
  ...                          // 既有 1~5
  string ws_token_secret = 6;  // 每拍携带（镜像 proxy 下发）；Worker 比对变化才应用+持久化
}
```

`ControlPlaneHandler`（[handler.go](../../../internal/controlplane/grpc/handler.go)）：加 `wsTokenSecret` 字段 + `SetWSTokenSecret(s)` 注入（同 `SetNodeProxyResolver` 风格）；`reregisterExisting`（:192）与 `createNewNode`（:238）的响应、`Heartbeat` 的每拍响应（:380 附近）均填充。未注入（零值）时不填，天然兼容既有测试装配。

### 3.3 Worker 侧接收 / 持久化 / 热应用

- `register.Result` / `register.Identity` 加 `WSTokenSecret`（json `wsTokenSecret`，随既有 0600 原子写）；`register.Register` 从响应透传。
- `setup.Run`（FR-222 路径）：首注册后把 `WSTokenSecret` 一并写入身份文件。
- `cmd/worker/main.go`：
  1. **启动时**把 `LoadIdentity` 提前到 WS 服务器构造前，初始生效密钥 = `identity.WSTokenSecret`（非空）> `cfg.JWTSecret`——消除「注册完成前用旧密钥」窗口；注册流程复用该次加载结果（不二次读文件）。
  2. **注册后**：响应 `WSTokenSecret` 非空且与当前不同 → `SetJWTSecret` 热应用到两个 WS 服务器 + `SaveIdentity` 持久化。
  3. **心跳**：`heartbeat` 包加 applier（镜像 `SetProxyRebuilder`，main.go:420）——响应值非空且变化 → 热应用 + 持久化。
- `ws.TerminalServer` / `ws.PluginBridgeServer`：`jwtSecret` 改为 `sync.RWMutex` 保护（或 `atomic.Value`）+ `SetJWTSecret(s)`；校验路径读锁取值。已建立的 WS 会话不受换密钥影响（握手只校验一次）。
- `worker.yml` 的 `jwt_secret` 保留为旧 CP 兼容回退，注释标注「新 CP 自动下发，通常无需配置」。

### 3.4 FR-276 诊断兜底

- `TerminalProxy.Handler`（[terminal_proxy.go:96](../../../internal/controlplane/service/terminal_proxy.go)）：`websocket.DefaultDialer.Dial` 的第二返回值 `*http.Response` 当前被弃——取之，`StatusCode == 401/403` 时向浏览器发结构化状态消息后关闭：
  ```json
  {"type":"state","state":"error","code":"WORKER_TOKEN_REJECTED","data":"终端令牌被 Worker 拒绝（HTTP 401）：该节点 WS 令牌密钥与平台不一致。新版本会经注册自动下发密钥，请确认 Worker 已升级并重启；手动部署请核对 worker.yml jwt_secret。"}
  ```
  其余 dial 失败维持现状文案（网络类）。
- 前端终端组件：state 消息带 `code=WORKER_TOKEN_REJECTED` 时展示上述诊断文案（视觉与普通断连区分），不再只显示「[连接已断开]」。
- `docs/DEPLOY.md`：加「终端/探针监控 401 排查」小节（密钥一致性、自动下发、存量探针 token 重发路径 FR-068、逃生口 `jwt.ws_secret`）。

### 3.5 兼容矩阵（设计约束）

| CP \ Worker | 新（本 FR） | 旧 |
|---|---|---|
| **新** | 自动下发，开箱可用 | 曾手动同步 jwt_secret 的部署会断（ADR-061 后果：同批升级 / `jwt.ws_secret` 逃生口）；未同步的本就坏，升级即修 |
| **旧** | 响应无字段 → 回退本地 `jwt_secret`，行为与现状一致 | 现状 |

## 4. 任务拆分

- [x] T1 CP：`ResolveWSTokenSecret` 三轨解析 + 单测（显式/dev/生产 autogen/复读稳定/损坏 fail）
- [x] T2 CP：config `jwt.ws_secret` + main.go 装配切三签发点 + settings 掩码项（+ configs 样例/docker-compose env 同步）
- [x] T3 proto：`RegisterResponse.ws_token_secret=3`、`HeartbeatResponse.ws_token_secret=6` + regenerate
- [x] T4 CP：handler `SetWSTokenSecret` + 注册两路径/心跳填充 + 单测（心跳**仅对已鉴权流下发**——未出示 node-secret 的旧版兼容流跳过鉴权，无门槛携带等于把密钥送给任何可达 gRPC 端口者；单测覆盖匿名流不下发）
- [x] T5 Worker：`register.Result`/`Identity` 加字段 + setup 持久化 + 单测
- [x] T6 Worker：WS 服务器 `SetJWTSecret`（锁保护）+ main.go 生效链（身份文件预载/注册后应用/心跳 applier + 持久化）+ 单测（**显式覆盖存量升级主路径：重注册响应带密钥 → 身份文件补写新字段**——现状仅首注册持久化，重注册路径必须新增持久化）
- [x] T7 FR-276 CP：代理 401 结构化诊断 + 单测
- [x] T8 FR-276 Web：`WORKER_TOKEN_REJECTED` 诊断文案渲染 + dom 测试
- [x] T9 文档同步：ARCHITECTURE（通信协议/密钥表/身份文件字段/数据根布局）、API、DEPLOY 排查节、CHANGELOG、architecture-invariants 增一行、configs 样例 + docker-compose、PRD 状态
- [ ] T10 真机走查（见 §5）

## 5. 验收标准

### FR-275
1. [x] 密钥三轨单测绿：显式配置优先；生产 autogen 落 `etc/ws-token-secret.key`（0600）且跨重启复读同值；dev 回退 `dev-secret-change-me`；密钥文件损坏/不可读 fail-fast。
2. [x] **隔离验证**（单测）：终端令牌仅专用 WS 密钥可校验、`jwt.secret` 校验必须不过（`TestWSTokenSecret_IsolatedFromUserSessionSecret` 钉签发侧契约；签发点与用户会话的装配分离由 main.go 保证）。
3. [x] 注册两路径（首注册/重注册）响应均含 `ws_token_secret`；心跳响应每拍携带（**仅已鉴权流**，匿名流不下发）；未注入零值不填（单测）。
4. [x] Worker：身份文件持久化/加载新字段；启动预载生效；注册后与心跳变化时热应用 + 持久化（单测含轮换场景：换密钥后新签 token 校验通过、旧 token 拒绝）。
5. [x] 旧 CP 兼容：响应无字段时 Worker 用本地 `jwt_secret`，既有测试全绿无回归（旧格式身份文件加载测试 + 全量套件）。
6. [x] `go vet` / `go test ./...` 全绿（既有无关红点除外，见 §6 注记）；web `tsc --noEmit` / `eslint`（改动文件）/ vitest（Terminal 相关 8 例）全绿；golangci-lint 以 `--no-config` 对触及包过滤本 FR 文件无新增违规（仓库 v1 配置与已装 v2 二进制不兼容，既有工具债，见 §6）。
7. [ ] **真机过（需用户确认）**：CP 生产态（`dev_mode=false`、不设 `jwt.ws_secret`）→ 一键安装全新 Worker（不手动设任何密钥）→ 开终端 101 可交互；探针插件桥连通、监控有数。CP 改显式 `jwt.ws_secret` 重启 → Worker **不重启**，≤1 心跳周期后新开终端正常（轮换自愈）。
8. [ ] 复现路径归真：worker 不设密钥 + CP 自定义 secret（原 401 复现步骤）在新版下终端正常。

### FR-276
1. [x] 代理 dial 收到 401/403 → 浏览器收到 `code=WORKER_TOKEN_REJECTED` 结构化消息；网络类失败维持原文案（单测覆盖两分支）。
2. [x] 前端展示诊断文案而非裸「连接已断开」（dom 测试）。
3. [x] DEPLOY 排查节落档。
4. [ ] **真机过（需用户确认）**：人为制造不一致（Worker 显式设错 `jwt_secret` 且 CP 下发被旧版模拟/关闭——用旧 Worker 二进制或临时改配置）→ 终端页显示明确诊断。

## 6. 风险 / 待定

- **存量探针长期 token 失效面**（ADR-061 后果）：两端升级后探针下次重连 401，需 FR-068 重发探针配置。DEPLOY 标注 + CHANGELOG 升级注意；不做自动重发（重建服/在线更新已是既有路径）。
- **新 CP + 旧 Worker（曾手动同步）断连**：靠同批升级 / `jwt.ws_secret` 逃生口过渡；CHANGELOG 升级注意标明。
- Windows 下 0600 语义有限：沿用项目既有惯例（ADR-052 后果同款），不额外做 ACL。
- 心跳持久化写盘频率：仅「值变化」才写身份文件，正常运行零额外 IO。
- Worker 降级兼容：旧二进制读新身份文件忽略 `wsTokenSecret`（encoding/json 读时忽略未知字段），但其后写盘会丢该字段——再升级时经注册/心跳重新下发自愈，无需人工干预。
- **CP 降级角**：Worker 已持久化下发密钥后，若 CP 降回旧版（签发回 `jwt.secret`），Worker 仍用持久化值校验 → 401（兼容矩阵「旧 CP 回退本地 jwt_secret」仅适用于从未收过下发的 Worker）。属罕见运维路径：FR-276 诊断会点明不一致，删身份文件 `wsTokenSecret` 字段或升回 CP 即恢复，不为此加代码。
- **验证时既有无关红点（2026-07-10，不由本 FR 引入/修复）**：① `internal/controlplane/router` 的 `TestFR034ProvisionRoutes`/`TestFR035ProvisionProxyRoutes` 因 mock 停留 Paper v2 API 形状而红（服务端已迁 fill v3，0df4f0b 漏迁 router 级 mock，已派独立修复任务）；② `go vet` 对 `bot_stress_session_test.go:147` 的 copylocks 告警（FR-274 已提交代码）；③ golangci-lint 仓库配置为 v1 格式、本机二进制 v2 不兼容（`can't load config`）。
