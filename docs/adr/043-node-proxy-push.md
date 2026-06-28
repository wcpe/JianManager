# ADR-043: 节点级出站代理由 CP 管控 + gRPC 下发 + Worker 运行时应用

- **日期**: 2026-06-28
- **状态**: accepted（随 FR-185 落地，扩充 ADR-037）
- **上下文**: ADR-037（FR-174）已把 CP 与每个 Worker 的出站下载统一收口到共享工厂 `internal/platform/httpclient`，各进程从自身 yaml `proxy:` 段读代理配置。但 ADR-037 明确把「面板配代理」「CP 下发代理给 Worker」「改 proto/gRPC」三件**列为范围外**——代理被定性为「进程本地出站行为，不涉及节点间 RPC」。实践中这带来运维痛点：国内部署常需为 CP 与每台 Worker 各登机器改 yaml 再重启，节点规模化后逐台维护既慢又易漏。运营需要在**面板一处**配全局/CP 代理、为单节点配覆盖，并**运行时热生效**，免登机器。

## 决策

**节点级出站代理由 Control Plane 统一管控（真相源 = CP DB），经 gRPC 心跳响应下发给 Worker，Worker 运行时重建出站 client 应用，不落盘。** CP 自身出站代理由设置面板的 settings DB 覆盖层管控、运行时重建。两者都不破坏 ADR-037 的 yaml/env 回退路径（向后兼容）。

1. **CP 自身出站（全局代理）**：设置面板新增「网络」分类（仅平台管理员），可编辑键 `proxy.url`（敏感、脱敏展示）、`proxy.no_proxy`，归入 settings 白名单（network 类）。保存写入 `platform_settings` DB 覆盖层，CP **运行时重建**出站持有者、免重启。此全局值同时作为**各节点的默认代理**。
   - 生效优先级：**settings DB 覆盖（全局） > `control-plane.yaml` proxy > 环境变量**（空配回退 `httpclient.New` 的 `ProxyFromEnvironment` 语义）。
2. **节点级覆盖**：`nodes` 表加性新增 `proxy_mode`（`inherit`|`custom`，默认 `inherit`）、`proxy_url`、`proxy_no_proxy`（仅 custom 时有效）。CP 据「节点 custom ? 节点值 : 全局默认」算出每节点**期望代理**。
   - 节点出站生效优先级：**节点自定义（DB 下发） > 全局默认（DB 下发） > `worker.yaml` proxy > 环境变量**。
3. **下发通道 = 心跳响应携带（不新增 RPC）**：`HeartbeatResponse` 加性新增 `proxy_url`、`proxy_no_proxy`、`proxy_generation`（期望代理配置的 FNV 哈希）。每次心跳 CP 据 DB 算期望代理填入响应；Worker 处理心跳响应，仅当 `proxy_generation` 与本地已应用代不同时才 `httpclient.New` 重建并替换出站持有者（避免每拍重建）。
   - **重连/重启天然重发**：Worker 仅启动时注册一次、但心跳持续；CP 每拍都据 DB 下发期望代理与 generation，Worker 重启后首个心跳即收到当前期望代理并应用。无需 Worker 落盘、无需额外协调——DB 是唯一真相源，Worker 不落盘为准。
4. **运行时持有者**：CP 与 Worker 的出站 client 都从「启动时 `httpclient.New` 一次」升级为 `httpclient.Provider`（内含 `atomic.Pointer[http.Client]`）。各下载点改为「每次从持有者取当前 client」，保存/下发新代理后替换持有者即对后续下载生效。
5. **脱敏与校验**：含 `user:pass` 的代理 URL 在 UI 回显、API 响应、日志中一律经 `httpclient.Sanitize` 脱敏。保存/下发前复用 `httpclient` 的 URL/scheme 校验，非法（不支持 scheme / 不可解析）即报错，不静默直连。

## 理由

- **为何下发而非沿用 ADR-037「各进程各配 yaml」**：ADR-037 的「各机各配」论断对**静态、登机器可改**的场景成立，但运营要的是**面板集中配 + 热生效 + 节点级差异**。下发让运营在一处配齐、按节点表达差异（custom/inherit），免逐台登机器改 yaml 重启——这正是 ADR-037 当初列为「范围外/后续」的演进，本 ADR 接手并落地。
- **为何真相源 = CP DB、Worker 不落盘**：符合架构不变量「数据库仅 CP 读写」「依赖方向 CP→gRPC→Worker」。Worker 持有的代理是 CP 下发的运行时状态，重连由 CP 依 DB 重发即可恢复，无需 Worker 落盘引入第二份真相、避免 DB 与节点本地文件不一致。
- **为何选心跳携带而非新增 SetProxy 单 RPC**：心跳是已有的、每拍都在的 CP↔Worker 双向流，重连天然重发、零新 RPC；用 generation 哈希比较避免每拍重建 client。新增单 RPC 虽更贴近「CP 发起 unary」范式，但需额外处理「重连后重推」的时序，复杂度更高、收益不明显。
- **为何保留 yaml/env 回退**：下发值为空（极早期未下发 / CP 未配全局且节点 inherit）时回退 `worker.yaml` proxy 再回退 env，保证既有部署与零配置行为完全不变（向后兼容）。

## 后果

- `internal/platform/httpclient` 新增 `Provider`（atomic client 持有者）+ `ProxyGeneration`（配置哈希）+ 单测；`Config` 不变。
- CP `SettingsService` 加 `proxy.url`/`proxy.no_proxy`（network 类、`proxy.url` 敏感脱敏）写白名单 + 校验（复用 `httpclient.New` 试构造）；保存后经回调重建 CP 出站持有者（优先级 settings DB > yaml > env）。CP `main` 把 `outboundClient *http.Client` 改为 `outboundProvider *httpclient.Provider` 注入各出站点（SelfUpdate/Asset/Core/...）。
- CP `model.Node` 加 `ProxyMode`/`ProxyURL`/`ProxyNoProxy`（加性迁移，默认 `inherit`）；新增 `PATCH /nodes/:id/proxy`（平台管理员 + 审计）。
- proto `HeartbeatResponse` 加 `proxy_url`/`proxy_no_proxy`/`proxy_generation`（加性，`make proto` 重生成）。CP `Heartbeat` 据 `EffectiveNodeProxy(node)` 填响应。
- Worker `heartbeat` 处理心跳响应：generation 变化时经回调 `httpclient.New` 重建并替换持有者；Worker `main` 把 `outboundClient` 改为 `Provider` 注入 JDK/CFR/selfupdate/jar 下载点（FR-174 已收口的注入点改为从持有者取）。
- 前端设置页新增 network 分类（`keyCategory` 把 `proxy.*` 归 network）；节点页新增「出站代理」面板（继承/自定义 + 脱敏展示）；i18n zh/en 加性补键。
- 错误透出代理地址时脱敏；节点离线时面板标注「待下发」（下次心跳生效）。

## 关系

- **ADR-037（每进程出站代理 + 共享工厂）**：本 ADR **扩充**之——保留其工厂设计与 yaml/env 路径，取代其「UI/下发/改 proto 列为范围外」的立场（代理从「进程本地、不下发」演进为「CP 管控 + 下发 + 运行时应用」）。ADR-037 不标 superseded（其工厂与回退仍生效），仅本 ADR 在其上叠加下发层。
- **FR-185（出站代理可视化配置）**：本 ADR 的落地 FR（增强 FR-174）。
- **架构不变量「通信协议」/「数据所有权」/「依赖方向」**：下发经既有心跳流（CP→Worker 方向），真相源 CP DB，Worker 不碰 DB、不落盘，不引入反向依赖，不违反三进程模型。
- **FR-063 / ADR-015（平台设置 DB 覆盖层）**：CP 全局代理复用 settings 覆盖层机制（白名单 + 优先级 DB > env > yaml）。
- **config-files 规范**：含凭据的 `proxy.url` 经 `${ENV_VAR}` 注入（yaml 路径）或经设置面板录入（DB 路径，脱敏存取）、不硬编码。
