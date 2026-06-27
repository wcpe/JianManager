# ADR-037: 每进程出站代理 + 共享出站 HTTP 客户端工厂

- **日期**: 2026-06-27
- **状态**: accepted（随 FR-174 落地）
- **上下文**: CP 与各 Worker 的所有出站下载（自更新 feed/二进制、JDK 归档、服务端 jar[Paper/Mojang/BungeeCord]、CFR 反编译器、未来 GitHub API）当前都直接用 `http.DefaultClient`（或散落的 `&http.Client{Timeout:...}`），**无可配置的出站代理**。部署在国内时连 GitHub / Adoptium / Mojang / Maven Central 常需代理；而出站点散落在多个包，无统一注入点，逐个改既易漏又难维护。同时 CP 与各 Worker 分布在不同机器、各自直接出站，网络环境（是否需代理、代理地址）天然不同，不应由一处全局配置统管。

## 决策

**为 CP 与每个 Worker 各加一套「每进程出站代理」配置，并把所有出站下载统一收口到一个「共享出站 HTTP 客户端工厂」`internal/platform/httpclient`。**

1. **每进程独立配置**：CP（`control-plane.yaml`）与 Worker（`worker.yaml`）各新增 `proxy:` 段，互不影响（分布式各机各配）。字段：
   - `url`：代理地址，scheme 决定类型（`http://` / `https://` / `socks5://`）；**留空 = 不显式配置**。
   - `no_proxy`：逗号分隔的免代理主机 / 域 / CIDR。
   - 含凭据的代理 URL 经 `${ENV_VAR}` 注入、不硬编码（config-files 规范）。
2. **共享工厂**：`httpclient.New(cfg) (*http.Client, error)` 按本进程代理配置构造 `*http.Client`：
   - `url` 空 → Transport.Proxy = `http.ProxyFromEnvironment`（**等价改造前行为**，仍尊重 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` 环境变量）。
   - `url` 为 `http://`/`https://` → Transport.Proxy 固定返回该代理，但遵守 `no_proxy`。
   - `url` 为 `socks5://` → 用 `golang.org/x/net/proxy` 构造 SOCKS5 dialer 挂到 `Transport.DialContext`；`no_proxy` 命中的主机走直连 dialer。
   - 非法 scheme / 不可解析 URL → 返回明确 error（启动时 fail-fast，不静默直连）。
   - `no_proxy` 判定统一复用 Go 既有语义（`golang.org/x/net/http/httpproxy` 的 `Config.ProxyFunc()`），http/https/socks5 共用一套，不自造。
3. **空配回退向后兼容**：留空 = `ProxyFromEnvironment`，保证零配置 / 旧部署行为完全不变。
4. **工厂只管 proxy/transport，不写死超时**：下载用长超时由各调用方控制（如自更新 10min、JDK 15min），工厂不掐断大文件下载。
5. **注入而非全局替换**：`selfupdate` 新增 `DownloadWith(ctx, client, ...)`，`Download` 保留为「用 `DefaultClient` 调 `DownloadWith`」的薄包装（向后兼容现有测试）；CP/Worker 生产路径在 `main` 装配工厂 client 并注入到各下载点（自更新、JDK 下载器、CFR provider、服务端 jar 下载、CoreService、AssetService）。

## 理由
- **按进程配**：CP 与各 Worker 在不同机器、各自直接出站，网络环境不同；CP 下发统一代理给 Worker 既增加 proto/gRPC 复杂度，又无法表达各机差异。每进程改自己的配置文件最简单、最贴合分布式现实。
- **统一工厂**：避免散落的 `http.DefaultClient` 各自为政导致「部分下载走代理、部分不走」；一个注入点，新增出站点也只需取这一个 client。
- **空配回退 `ProxyFromEnvironment`**：现状即用 `DefaultClient`（其 Transport 默认就是 `ProxyFromEnvironment`），故空配回退它 = 行为不变 + 仍尊重 env 代理，零配置部署不受影响。
- **SOCKS5 经 `x/net/proxy`**：标准库 `http.Transport.Proxy` 只支持 http/https/socks5(部分版本) 经 `Proxy` 字段，SOCKS5 拨号统一用成熟的 `x/net/proxy`（已在依赖树）更可控，且能与 `no_proxy` 直连 dialer 并存。

## 后果
- 新增 `internal/platform/httpclient` 包（`Config` + `New` + 单测）。
- CP `config.Config` 加 `Proxy httpclient.Config`（mapstructure `proxy`）+ viper 默认空 + `control-plane.yaml` 样例段。
- Worker `config.Config` 加 `Proxy httpclient.Config`（**加性追加**，不动既有字段，降低与 FR-080 的 rebase 冲突）+ `Load` 默认空 + `worker.yaml` 样例段。
- `selfupdate` 加 `DownloadWith`；CP `SelfUpdateService`、Worker gRPC `Server`、`jdk.Manager`、`decompiler.Provider`、`CoreService`、`AssetService`、provision `downloadFile` 全部改走注入的工厂 client。
- 错误透出代理地址时**脱敏 `user:pass`**（只留 host:port），便于运维定位又不泄露凭据。
- 启动时 `proxy.url` 非法 → CP/Worker fail-fast。

## 关系
- **FR-174（出站网络代理）**：本 ADR 的落地 FR。
- **FR-175（自更新对接 GitHub Releases）**：其 GitHub API 与二进制下载将复用本工厂出站（本 ADR 是其前置）。
- **架构不变量「通信协议」**：代理是**进程本地出站行为**，不涉及节点间 RPC（不改 gRPC / proto），不违反三进程模型与依赖方向。
- **config-files 规范**：含凭据的 `proxy.url` 经 `${ENV_VAR}` 注入、不硬编码。
