# 功能规格：出站网络代理（每进程 HTTP/SOCKS5）

> 状态：草拟　·　关联 PRD：FR-174　·　关联 ADR：ADR-037（本 FR 创建）　·　分支：feature/fr-174-network-proxy

## 1. 背景与目标

CP 与 Worker 的所有出站下载（自更新 feed/二进制、JDK、服务端 jar、CFR 反编译器）当前都用 `http.DefaultClient`，**没有可配置的出站代理**。部署在国内时连 GitHub / Adoptium / Mojang 等常需代理。本 FR 为 CP 与每个 Worker（分布式、各自出站）各加一套**可配置的出站代理**（HTTP/HTTPS/SOCKS5），并把所有出站下载统一收口到一个**共享出站 HTTP 客户端工厂**。P1。这是 FR-175（自更新对接 GitHub）的前置：FR-175 的 GitHub API 与二进制下载将经本工厂出站。

## 2. 需求（要什么）

- **每进程独立配置**：CP（`control-plane.yaml`）与 Worker（`worker.yaml`）各新增 `proxy:` 配置段（CP 与各 Worker 在不同机器，各配各的）。
- **字段**：`url`（代理地址，scheme 决定类型：`http://` / `https://` / `socks5://`；空=不显式配置）+ `no_proxy`（逗号分隔的免代理主机/域/CIDR）。敏感信息（含凭据的代理 URL）支持 `${ENV}` 注入（config-files 规范）。
- **不破坏现状**：`url` 留空时**沿用当前行为**（`http.ProxyFromEnvironment`，即仍尊重 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` 环境变量）——零配置/旧部署行为不变；`url` 非空时用配置的代理（优先于环境变量）。
- **共享出站客户端工厂**：新增包，按本进程代理配置构造 `*http.Client`/`*http.Transport`；SOCKS5 经 `golang.org/x/net/proxy`（x/net 已是依赖）。
- **接入全部出站点**：把现有所有出站 `http.DefaultClient`/`http.Get`/自建 client 的下载点改走工厂 client。已知点（agent 须 grep 全仓库确认无遗漏，不止这些）：
  - `internal/platform/selfupdate.Download`（CP 自升 + Worker 升级共用，FR-175 也将复用）
  - `internal/controlplane/service/selfupdate.go` 的 `FetchFeed`（feed/未来 GitHub API 出站）
  - JDK 下载（`internal/worker/jdk/…`）
  - 服务端 jar 下载（Paper/Mojang/Spigot 等，provision 相关；CP 或 Worker 侧，grep 定位）
  - CFR 反编译器下载（`internal/worker/decompiler/cfr.go`，`embed-cfr` 的运行时回退路径）
- 范围内：proxy 配置段（CP+Worker）+ 工厂包 + 接入现有出站点 + ADR-037。
- 不做（范围外）：
  - 入站/反向代理；MC 代理（BungeeCord/Velocity，FR-035 已有，语义无关，勿动 `proxyconfig.go`/`proxy.go`/`terminal_proxy.go`）。
  - per-下载源不同代理（一个进程一套出站代理即可）。
  - 面板 UI 配代理 / CP 下发代理给 Worker（用户选「每进程全局代理」，各进程改配置文件即可；UI 化留后续）。
  - 改 proto / gRPC（代理是进程本地出站行为，不涉及节点间协议）。

## 3. 设计（怎么做）

### 3.1 ADR-037（本 FR 创建）

「**每进程出站代理 + 共享出站 HTTP 客户端工厂**」决策：为何按进程配（分布式各机网络环境不同）、为何统一工厂（避免散落的 `http.DefaultClient` 各自为政）、空配回退 `ProxyFromEnvironment`（向后兼容）、SOCKS5 经 x/net/proxy。决策正文写 ADR，勿在 spec 重复。

### 3.2 共享工厂 `internal/platform/httpclient`

- `type Config struct { URL string; NoProxy string }`（mapstructure `url`/`no_proxy`）。
- `func New(cfg Config) (*http.Client, error)`：
  - `url` 空 → 返回 transport 用 `http.ProxyFromEnvironment` 的 client（等价现状 + 尊重 env）。
  - `url` 为 `http://`/`https://` → `http.Transport{ Proxy: <固定返回该 URL，但遵守 no_proxy> }`。
  - `url` 为 `socks5://` → 用 `golang.org/x/net/proxy` 构造 SOCKS5 dialer，挂到 `Transport.DialContext`；`no_proxy` 命中的主机走直连 dialer。
  - 非法 scheme / 不可解析 URL → 返回明确 error（启动时即报，不静默直连）。
  - `no_proxy` 解析：复用 Go 既有语义（可借 `http.ProxyFromEnvironment` 背后的 `httpproxy.Config{HTTPProxy,HTTPSProxy,NoProxy}.ProxyFunc()`，`golang.org/x/net/http/httpproxy`），统一 http/https/socks5 的 no_proxy 判定，避免自造。
- 合理默认超时透传（下载用长超时由调用方控制，工厂只管 proxy/transport；勿在工厂写死短超时把大文件下载掐断）。
- 纯逻辑可单测。

### 3.3 配置接入

- **CP**：定位 control-plane 配置 struct（`configs/control-plane.yaml` 对应的 Go struct，约在 `internal/controlplane/config` 或同名包）→ 加 `Proxy httpclient.Config`（mapstructure `proxy`）+ viper 默认（空）+ `configs/control-plane.yaml` 样例注释段。
- **Worker**：`internal/worker/config.go` 的 `Config` 加 `Proxy httpclient.Config`（mapstructure `proxy`）+ `Load` 里 `v.SetDefault("proxy.url","")`/`v.SetDefault("proxy.no_proxy","")` + `configs/worker.yaml` 样例注释段。
  - 注意与 FR-080 收口同改 `worker.yaml`/`config.go`：本 FR 仅**加性**追加 `proxy:` 段与 `Proxy` 字段，不动既有字段，降低 rebase 冲突。
- **注入**：
  - Worker 侧：`cmd/worker/main.go` 用 `httpclient.New(cfg.Proxy)` 造进程级 client，注入到 selfupdate（Worker 升级下载）、JDK 下载器、CFR provider、jar 下载点。
  - CP 侧：用 `httpclient.New(cfg.Proxy)` 注入到 `SelfUpdateService`（`FetchFeed` + CP 自升下载）、CP 侧 jar 下载（若有）。
- **selfupdate.Download 签名**：当前 `Download(ctx, url, sha256, dest, allowInsecure)` 用 `http.DefaultClient`。改为可注入 client——推荐新增 `DownloadWith(ctx, client, url, sha256, dest, allowInsecure)`，保留 `Download` 为「用 DefaultClient 调 DownloadWith」的薄包装（向后兼容现有测试），CP/Worker 生产路径改调 `DownloadWith` 传工厂 client。`FetchFeed` 同理改用注入 client。

### 3.4 错误与可观测

- 代理不可达/握手失败 → 下载/请求返回的 error 透出代理地址（**脱敏 user:pass**，只留 host:port），便于运维定位；不静默回退直连。
- 启动时若 `proxy.url` 非法，CP/Worker 启动即 fail-fast 报错（配置错误早暴露）。

## 4. 任务拆分

- [ ] 写 `docs/adr/037-outbound-proxy.md`（ADR-037）
- [ ] `internal/platform/httpclient` 包（`Config`/`New`，HTTP/HTTPS/SOCKS5/no_proxy/空配回退 env）+ 单测
- [ ] CP 配置 struct + `control-plane.yaml` 样例 + 默认值
- [ ] Worker 配置 struct（`config.go` 加 `Proxy`，加性）+ `worker.yaml` 样例 + 默认值
- [ ] `selfupdate.DownloadWith`/注入 client 改造（保留 `Download` 兼容）+ `FetchFeed` 注入
- [ ] 接入 JDK / 服务端 jar / CFR 下载出站点（grep 全仓库确认无遗漏）
- [ ] CP/Worker main 装配工厂 client 并注入各下载点
- [ ] doc-sync：PRD §4 FR-174 状态「计划」→「开发中」（只改本行）；ARCHITECTURE「出站网络/配置」章节；CONVENTIONS/config-files 配置项；CHANGELOG `[Unreleased]` 末尾追加一行（只加不改）；ADR-037
- [ ] 中文 commit（`feat(worker)`/`feat(control-plane)`/`feat(config)` 按 git-commit 规范，按模块拆 commit）

## 5. 验收标准

- `httpclient` 单测绿：空配→`ProxyFromEnvironment`；`http://` 代理被使用；`socks5://` 构造正确 dialer；`no_proxy` 命中主机走直连；非法 URL 报错。
- CP 与 Worker 配置加载 `proxy` 段，`${ENV}` 注入与 `JIANMANAGER_PROXY_URL` 类 env 覆盖生效（沿用既有 viper env 覆盖惯例）。
- 既有出站点（selfupdate.Download/FetchFeed、JDK、jar、CFR）改走工厂 client：编译通过 + 既有相关测试仍绿 + 代码审查确认无残留 `http.DefaultClient`/裸 `http.Get` 出站。
- 空配下行为与改造前一致（直连/尊重 env 代理），不破坏现状。
- **【需真机，用户确认】** 在需代理环境：配置 `http://` 代理后自更新/JDK 下载成功；配置 `socks5://` 代理同样成功；`url` 留空则直连成功（至少 HTTP 与 SOCKS5 各验一条）。

## 6. 风险 / 待定

- **出站点遗漏**：必须 grep 全仓库所有出站 http 构造点（`http.DefaultClient`、`http.Get`/`http.Post`、`&http.Client{`、`http.NewRequest` 后用 DefaultClient 的），逐一收口；遗漏=部分下载不走代理。
- **与 FR-080 同改 worker 配置**：本 FR 对 `config.go`/`worker.yaml` 仅加性追加 `proxy`，FR-080 收口不重写该 struct（已确认 config 加载已实现）→ rebase 冲突小且易解（保留双方新增）。rebase 顺序建议 FR-080 先、FR-174 后（见并行计划）。
- **SOCKS5 依赖**：`golang.org/x/net/proxy` / `golang.org/x/net/http/httpproxy`——确认已在 go.mod（`selfupdate.go` 已 import `golang.org/x/net/context`，x/net 在树）；若需显式加 `go get` 一次。
- **真机需代理环境**：agent 无代理环境时单测 + httptest 覆盖逻辑，真机两条标「待真机验」由用户确认。
