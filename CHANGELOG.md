# CHANGELOG

> 本文档累积更新，每次发版新增一个版本段。

---

## [Unreleased]

### 修复
- **探针跨平台注入崩溃（taboolib-ioc 1.2.1-SNAPSHOT，FR-066 深层真机修复）**：ServerProbe 子模块依赖升至 taboolib-ioc 1.2.1-SNAPSHOT，修复带跨平台类入参事件监听器（FR-066 玩家事件监听器）在错误平台注入时反射 `declaredMethods` 触发 `NoClassDefFoundError`（Bukkit 上加载 Bungee `PostLoginEvent`）导致整个探针 enable 失败被 Disabling 的崩溃。根因是该 IoC 经 `getRunningClassesInJar` 直读类表、绕过了 TabooLib 自带的 `@PlatformSide` 过滤。主修复：object 注入器在收集阶段按 `@PlatformSide` 与 `Platform.CURRENT` 比对、**主动跳过错误平台宿主**（根本不反射，从源头规避）；并保留注入路径对 `NoClassDefFoundError` 的防御性捕获（`findAnnotationCarrier` 反射加 `try-catch`、`injectObjectFields` 改 `catch(Throwable)`）兜底未标注解却引用缺失类的情形。真机验证：Paper 1.21.1 探针正常 enable，全程 0 `NoClassDefFoundError`、插件桥连入、玩家名册/踢人端到端正常。
- **插件桥 token 改为实例生命周期有效期（FR-066/067 深层真机修复）**：CP `pluginBridgeTokenTTL` 由 10 分钟改为约 10 年（等效实例生命周期）。插件桥 token 是写入探针 config.yml、整个生命周期复用的**持久连接凭据**（普通重启不重新下发 config，仅建服/FR-068 在线更新时下发），原 10 分钟 TTL 导致建服数分钟后任何重启/重连都因 token 过期被 Worker 握手拒绝（401）、桥永久连不上。安全上桥仅本机回环可达、token 按实例隔离且落在本机 config 文件，短 TTL 既挡不住实质重放又必然弄坏重启。真机验证：桥连接 + 重连不再 401。
- **插件桥接管 ping 刷新读 deadline 防空闲误断（FR-065/066 深层真机修复）**：Worker `bridge.go` 补 `SetPingHandler`。探针按心跳节奏发 WS ping 控制帧，gorilla 默认 handler 仅回 pong、不刷新读 deadline，且控制帧不让 `ReadMessage` 返回，故无玩家活动的空闲桥连接每约 90 秒被误判断线重连（扰动 FR-066 实时事件流）。接管 ping handler：收 ping 即刷新读 deadline 并回 pong。真机验证：空闲桥连接 >120s 稳定无断开。

## 0.7.0（2026-06-22）

### 新增
- **创建/编辑模态框体验统一**（FR-072）：横扫所有创建/编辑对话框，统一为「高度自适应 + 可编辑下拉 + 字段校验 + 必填提示」。新增可复用基建：高度自适应滚动壳（`scrollable-dialog`：shadcn DialogContent 限高 + 正文滚动、头脚固定；裸 div 模态框统一遮罩/面板类 `max-h-88vh` 内滚，短视口可滚动提交）、可编辑下拉框 `Combobox`（已知集下拉 + 允许自定义输入，Radix Popover，主题自适应）、字段校验纯函数库（必填/端口/绝对路径/URL/正整数/环境变量引用/主机名 + 最小长度，含 vitest）、必填(*)标记与内联错误组件。系统可获取项改 Combobox（节点/JDK/核心类型/MC 版本/群组/代理类型/模板/后端/实例/备份存储类型/JDK 厂商·架构，ID 绑定项禁自定义、字符串项允许自定义）；提交前阻断 + 错误内联提示 + 必填/选填明确。覆盖实例/搭服/搭代理/代理后端注册/克隆/标签/建组/建用户/建群组(软标签)/告警规则/Bot（控制台+全局）/模板/定时任务/备份存储/JDK 登记。i18n validation/combobox 键 zh/en 对齐。纯前端，不改后端行为。
- **文件管理资源管理器化 + 共享编辑器组件**（FR-070）：实例「文件」段从平铺单文件浏览重做为双栏资源管理器 `components/explorer/ResourceExplorer`——左懒加载目录树 + 右目录内容/ CodeMirror 编辑器，作为 FR-071/073/074/075 等复用地基。交互全集：新建文件/夹、重命名、删除、剪切复制粘贴、树内拖拽移动（复用既有 `rename`，跨目录即移动）、拖拽上传 + 按钮批量上传、下载（单文件流式 + 多选/目录打 zip）、多选（shift 连选 / ctrl 点选 / 全选 → 批量删·下·移）；编辑器多格式高亮（yaml/json 专用包，properties/ini/cfg/conf/toml 轻量 StreamLanguage，其余纯文本兜底，不引 Monaco）+ Ctrl+S 拦截保存接 FR-051 历史；历史版本经右侧抽屉（版本/diff/回滚），删除·回滚走统一 `DangerConfirm` 二次确认（FR-059）。后端仅新增批量打包能力：Worker gRPC `DownloadArchive`（边遍历边 zip 分片流式、每条目防 zip-slip）+ CP `POST /instances/:id/files/archive`（流式代理为 `application/zip`）。选择/剪贴板/路径/语言映射抽为纯函数并补 vitest 覆盖。
- **实时插件桥通道地基**（FR-065 / ADR-016，取代 ADR-014「探针只读+RCON 治理」、复活并扩展 ADR-012 的 WS 通道）：打通 ServerProbe fork 经**反向 WebSocket** 主动连入本机 Worker 的实时双向通道，为玩家事件/治理/在线更新/全状态查询铺底。Worker 重建 `/ws/plugin-bridge`（与 `/ws/terminal` 并列同监听端口）+「实例 UUID→探针会话」表（单活动会话、新连顶替旧连）+ token 握手校验（复用 JWT secret，校验签名 + `scope=plugin-bridge` + token 内 instanceId 与 query 一致）+ ping/pong 心跳与读超时断线判定 + connected/disconnected 冒泡；CP 新增实例级 plugin-bridge token 签发（HS256，类比终端 token），随探针 config 的 `bridge:` 段下发（worker WS 回环地址+实例+token，签发失败优雅降级不阻断建服、`/metrics` 不受影响）；proto 一次铺齐桥全面（gRPC `StreamPluginEvents`/`SendPluginCommand`/`QueryServerState` + `PluginEvent`/`PluginCommand` 事件命令骨架，经 protoc 重新生成 workerpb，下游 FR-066/067/068 复用不再改 proto）；ServerProbe fork core 模块新增 `BridgeClient`（IOC `@Service`）+ 零三方依赖的最小 RFC 6455 客户端 `MinimalWebSocketClient`（JDK 8 兼容：HTTP Upgrade 握手 + 帧编解码 + 客户端掩码），连入→发 hello→周期 ping 心跳→发 demo connected 事件→断线指数退避重连，token 绝不入日志。地基阶段仅打通通道层，玩家事件/治理执行/退役 RCON 留 FR-066/067。真机端到端（真 Paper + 探针 fork 连入真 Worker）待真机验。
- **配置管理资源管理器化 + 自动发现全部配置 + 收藏**（FR-071）：实例「配置」段从独立三栏 `ConfigEditor` 重做为**复用 FR-070 `ResourceExplorer`** 的 `components/config-explorer/ConfigExplorer`——直接获得资源管理器交互全集（左树右内容/编辑器、重命名/多选/批量/拖拽/剪切粘贴/移动/新建/删除/上传/下载）。叠加配置专属能力：打开文件改用 `ConfigFileEditor`（schema 文件保留**表单/文本双模式**、表单走字段级补丁保留注释，非 schema 纯文本 + 多格式高亮复用共享 `CodeEditor`，**Ctrl+S 保存即生成配置版本** FR-031，**跨文件一致性校验**保留）；左栏顶部 `FavoritesBar` 提供**收藏书签**（按实例存 `localStorage`）+ **已发现配置**面板（递归发现工作目录下全部实际配置文件，不限内置 schema 那 6 个，按目录分组 + 筛选 + 一键收藏/打开）；历史经 `ConfigVersionDrawer`（FR-031 配置版本/diff/回滚，与文件版本表分离）。`ResourceExplorer` 加可选 `config` 能力注入（编辑器插槽 / 左栏插槽 / 配置版本抽屉 / 按路径打开），不注入时文件段行为完全不变。后端仅新增加性端点 `GET /instances/:id/configs/discover`（CP 经既有 `Worker.ListFiles` gRPC 逐目录递归遍历 + `isConfigFile` 过滤 + schema 命中标记，深度/目录数上限保护，**不改 proto**）；递归遍历核心抽为纯函数 `walkConfigPaths` 并补 Go 表驱动单测，收藏/发现分组抽为纯函数 `favorites.ts`/`discover.ts` 并补 vitest。i18n `configExplorer.*` zh/en 对齐。真机（发现全部配置 / 编辑非 schema / Ctrl+S 存 + 配置版本回滚 / 收藏 + 重命名 + 多选）待真机验。
- **实时玩家事件 + 精确跨服感知**（FR-066 / ADR-016，复用 FR-065 插件桥）：玩家进出、聊天与跨服路由经探针实时推送到浏览器。ServerProbe fork 子服端 `BukkitPlayerEventListener`（监听 PlayerJoin/Quit/AsyncChat）报本子服 join/quit/chat，代理端 `BungeePlayerEventListener`（监听 PostLogin/ServerSwitch/PlayerDisconnect）报精确跨服路由（from→to），均经 core `BridgeClient.emitPlayerEvent`（嵌套 `data` 结构化字段、未连接静默丢弃、任意线程安全、绝不阻塞服务器）反向 WS 上报，插件桥关闭时不上报。Worker 侧 `bridge.go` 解析 event 帧结构化字段（玩家名/UUID/消息/子服/from·to）填充 `workerpb.PluginEvent` 冒泡（proto 字段已就位，未改 proto）；CP 新增 `PlayerEventService` 订阅各 Worker 的 `StreamPluginEvents`，维护「实例 UUID→实时在线名册」（connected 重置 / player_join 加入 / player_quit 移除 / cross_server 更新所在子服 / disconnected 清空），经 SSE `GET /instances/:id/players/events`（首帧 `init` 含连接状态 + 名册快照，之后 `player` 增量、按实例 UUID 过滤）推前端。前端玩家页新增「实时事件」标签：选实例 → SSE 驱动的实时在线名册（标注所在子服）+ 事件流面板（类型徽标 + 跨服 from→to）+ 探针未连入降级提示。i18n zh/en 对齐。真机（真 Paper + 真 BC，玩家进/切/退/发言端到端）待真机验。
- **玩家治理迁探针 + 退役 RCON 全链路**（FR-067 / ADR-016）：玩家治理（踢/封/解封/白名单/在线列表）从 RCON 文本协议切到 ServerProbe 插件桥——CP→gRPC `SendPluginCommand`→Worker→反向 WS→探针执行平台 API（Bukkit/BC `BridgeCommandHandler` 经服务端 API 执行 kick/ban/whitelist），在线名册改探针事件聚合（跨服）。**完全退役 RCON**：删除 Worker `metrics/rcon.go`+`grpc/rcon_ops.go`、端口池 RCON 分配、schema 跨文件校验的 rcon.port、`GetInstanceMetrics` 的 RCON 回退；指标改纯探针（未部署=N/A+「需部署探针」提示）；实例模型 `RCONPort/RCONPassword` 字段保留但标 Deprecated（迁移安全）。FR-022（RCON 指标采集）→ deprecated；FR-054 治理执行路径更新为探针。RCON 相关单测删/改写为探针路径。真机（经探针真机踢/封/白名单）待真机验。
- **探针在线更新**（FR-068 / ADR-016）：平台「点一下」把 CP 内嵌（`go:embed`，FR-010）最新 ServerProbe jar 经已有 gRPC `DeployServerProbe` 推到实例 plugins 目录（**下次重启生效**），可选「更新并重启」推送后立即重启实例使其生效。新增 `GET /instances/:id/probe/update`（探针连接状态 + 内嵌版本/指纹 + 上次推送时间）与 `POST /instances/:id/probe/update`（`restart` 可选，审计 `instance.probe.update`）；未内嵌探针 jar 时 `422 PROBE_NOT_EMBEDDED`。前端监控段新增「探针更新」卡（连接/版本/上次推送 + 更新/更新并重启）。复用既有 `DeployServerProbe`，未改 proto/子模块。i18n zh/en。真机（改 jar→点更新→重启后新版本连入）待真机验。

### 变更
- **实例导航与侧栏树形优化**（FR-069）：ConsoleSidebar 节点切换下拉**瘦身**为紧凑控件（矮行高 + 小字号 + 前置节点图标），实例分组从平铺小标题改为**可折叠树形分支**（按 节点/环境/状态 层级展开折叠，默认按节点；每分支头部显示计数，成员行保留状态点 RUNNING 绿·STARTING/STOPPING 琥珀·CRASHED 红·STOPPED 空心 + Bot 聚合徽标）；**折叠记忆**沿用 console store `collapsedGroups`（树分支键 `tree:<dim>:<group>` 与导航组 key 隔离）；**折叠优先**——折叠分支不渲染成员行，大量实例下不全量铺开、不卡。点实例开终端、节点切换（复用 `GET /instances?nodeId=`）、bot 徽标等既有能力无损。纯前端，i18n zh/en 对齐。
- **日志默认级别改为 INFO**：`configs/control-plane.yaml` / `configs/worker.yaml` 默认 `log.level` 由 debug 改为 info，默认配置启动不再 debug 刷屏。

### 修复
- **实例委托后台 goroutine 生命周期管理消除 drain 竞态**：`InstanceService` 加 `bgCtx/bgCancel/bgWG/bgMu`，fire-and-forget 的 Worker 委托改可 join 的 `spawnDelegate` + `Shutdown`（main 装配 `defer Shutdown`、测试 helper 禁用后台委托），修复 drain 后台 goroutine 在测试关库后仍写库的竞态（`TestNode_Drain_StopsRunning` 整包跑偶发失败）。
- **节点 JDK 弹窗标题与关闭按钮接入 i18n**：NodesPage JDK 管理弹窗 `JDK Management`/`Close` 硬编码英文改走 `nodes.jdkTitle` / `common.close`。
- **玩家页文案 RCON 残留改为探针桥**：players 页 subtitle/degraded/whitelistUnavailable 三处描述从 RCON 改为 ServerProbe 探针（FR-067 退役收尾，真机验收发现）。

## 0.6.0（2026-06-22）

### 新增
- **审计日志筛选 UI**（FR-015 归真）：审计页补全顶部筛选栏（用户下拉/操作/目标类型/时间范围），筛选下沉到后端 DB（`GET /audit?userId=&action=&targetType=&from=&to=&limit=`），变更即重查、「加载更多」递增 limit、「清空」恢复默认；时间按后端 `time.RFC3339` 期望转换（datetime-local 本地值经 `toISOString` 带时区透传）。套 FR-061 高密度风格，i18n zh/en 对齐。纯前端，不改后端行为。
- **定时任务管理 UI 归真**（FR-012）：定时任务页此前仅只读列表，补齐创建/编辑/删除对话框（创建走 `POST /schedules`、编辑走 `PUT /schedules/:id` 改 cron/动作/启用、删除走危险确认 `DELETE /schedules/:id`）、行内启停切换、执行日志行展开（`GET /schedules/:id/logs`，列时间/动作/结果/输出）与 Cron 表达式基本前端校验；页面套 FR-061 高密度风格（Panel + 密集表 + StatusBadge）。i18n zh/en 补全。
- **模板管理 UI 与模板删除**（FR-064）：模板页补「新建模板」对话框（名称/类型/描述/启动命令/下载URL/默认工作目录，接已有 `POST /templates`）与每卡「删除」（DangerConfirm 危险确认，接新增 `DELETE /templates/:id`）；模板与实例为松关联（建实例时拷贝 startCommand），删除模板不影响已创建实例。后端补 `DELETE /templates/:id`（service + handler + 路由 + 单测/路由测试）。模板页按 FR-061 高密度风格重写（Panel 卡片 + 工具栏标题，替换旧 `text-2xl` 大标题）。i18n zh/en。
- **平台设置：全量配置可视化与运行时调整**（FR-063 / ADR-015）：在 YAML+env 基线之上新增一层平台配置 DB 覆盖层（`platform_settings` 键值表），生效优先级 **DB 覆盖 > 环境变量 > YAML 默认**。新增 `GET /settings`（返回可编辑项 + 只读项当前生效值，敏感项 jwt secret / db dsn 脱敏不下发明文）与 `PUT /settings`（仅平台管理员，仅白名单键可改：日志级别 / JDK 下载镜像源 Temurin·Corretto·Zulu / 优雅停止超时 / 默认备份保留天数；非白名单键或非法值整体拒绝 422 且不落库）。可编辑项均接到真实读取点真生效：日志级别经 slog `LevelVar`（CP 内落库即时生效、重启自动重放）；JDK 镜像源、优雅停止超时经扩展 gRPC（`InstallJDKRequest.mirror_base` / `CreateInstanceRequest.graceful_stop_timeout_seconds`，protoc 重新生成）由 CP 读设置随安装/启动下发 Worker（请求值优先、回退 env；优雅停止对设置变更后新启动的实例生效）；默认备份保留天数经 CP 定期裁剪任务回收超期备份。前端系统设置页重构为「内部侧边栏 + 分类」（外观 / 日志 / 运行时 / 备份 / 安全·系统），可编辑项表单（按改动批量保存）+ 只读项展示（标注「需改配置并重启」、敏感项标注「已脱敏」），i18n zh/en 对齐。真机验收：改假镜像源 → worker 实下载走该 URL；改优雅停止超时 → 进程按新值退出。

### 变更
- **Go module path 修正**：`github.com/wxys233/JianManager` → `github.com/wcpe/JianManager`，同步全部 import / Makefile / proto `go_package`，并重新生成 `worker.pb.go`（修正先前 sed 改路径导致的 protobuf 描述符长度前缀损坏，该问题在本版未发布窗口内引入并修复）。影响从源码构建与下游导入者。

### 修复
- **监控图表在 0 尺寸容器渲染告警**（BUG-007）：`TimeSeriesChart` 在隐藏/未激活分段或折叠面板（0 尺寸容器）内、以及 `ResponsiveContainer` 自身首帧测量完成前，recharts 反复报 `width(-1)/height(-1) ... should be greater than 0`（×9）。改为 callback ref + `ResizeObserver` 实测容器宽度，直接以确切像素宽渲染 `LineChart`（弃用 `ResponsiveContainer`）；宽度为 0 时不渲染、获得尺寸后自动恢复，彻底消除 -1 告警，不影响总览/节点/实例监控既有图表。
- **加载期一条无谓的 401 请求**（BUG-008）：登录态下页面加载时，首个携带「已过期但可刷新」access token 的请求会先打出一条 401（再由拦截器刷新重试），该网络错误被浏览器记入控制台。改为请求拦截器在发请求前主动判过期并刷新（`isTokenExpired` + 共享 refresh 闸去重，与既有响应 401 兜底复用同一刷新路径）；绕过 axios 的 SSE 事件流连接前经 `ensureFreshToken` 同样预刷新。正常登录态控制台不再出现加载期 401。

## 0.5.0（2026-06-21）

### 新增
- **时序监控与历史曲线**（FR-060）：在 ServerProbe 富实时指标之上沉淀历史时序。Worker 30s 心跳附带节点指标（含 load average）+ 每实例 ServerProbe 快照（TPS/MSPT/堆 used·max/线程/CPU/uptime + 分世界 区块/实体/方块实体），Control Plane 经 `IngestHeartbeat` 落库并分级降采样（raw ~48h / 5m ~30d / 1h ≥1y，ADR-013）。新增 `GET /metrics/series`（节点/实例历史曲线，按区间 1h~90d 自动选档 raw/5m/1h）与 `GET /metrics/overview`（跨节点聚合总量 + 趋势）；探针不可达时段曲线断点（null）不显假值；probe 端口经 `CreateInstance` 下发并持久化 daemon PID 记录，Worker 重启 `RecoverDaemonInstances` 恢复自采。真机验证：真 Paper + ServerProbe 历史曲线累积、CP 重启不丢、5m 卷积、杀 worker 重启后采集无缝续上。
- **面板信息密度与视觉改造**（FR-061）：参考 baota 把前端重做为高密度运维面板。常驻**多级侧栏**（分组可展开，整合原三段式；实例树/节点切换器并入「实例」组、用户/组/审计并入「设置」组，能力不丢）；**环形资源仪表盘** + 分区面板 + 密集表格 + 迷你资源条 + 状态徽章，资源/TPS 按阈值自动变色；引入状态色系（success/warning/danger/info）与 **MC 绿主色**，替代纯灰阶。新增通用组件 `ResourceGauge`/`Panel`/`MiniBar`/`StatusBadge` 与历史曲线 `TimeSeriesChart`/`RangePicker`（recharts，多序列 + null 断点）。总览旗舰页（仪表盘 + 跨节点聚合曲线 + 密集实例表）、节点详情（仪表盘 + CPU/内存/磁盘/网络曲线 + 各实例指标对比）、实例工作区「监控」段（实例历史曲线 + 分世界）。纯前端重构，仍基于 shadcn/ui + Tailwind + OKLCH，不改后端行为。暗/亮主题 + zh/en 真机验证。
- **节点负载（load average）采集与仪表盘**（FR-062）：节点心跳采集系统负载（gopsutil 跨平台；Windows 经处理器队列长度模拟），Control Plane 落 `node_load` 时序 + 节点当前值，总览/节点详情新增「负载」环形仪表盘（按 CPU 核数归一后阈值变色）+ 历史曲线；取不到时优雅留空。真机验证：CPU 过载时 load 端到端落库与渲染。

### 修复
- **实例标签为 JSON 字符串致 /instances 白屏**（FR-047 回归）：后端 `tags` 等 JSON 列以原始字符串返回（空为 `""`），前端误当数组直接 `.filter` 抛 `TypeError` 致整页白屏；新增 `parseTags` 容错解析（数组/JSON 串/空/null），分组与标签编辑统一经其消费。
- **前端测试与静态检查清零**：修复 vitest 两处失败（`auth.ts` 模块顶层 localStorage 访问加非 DOM 守卫、`bot-list` 过期断言）；清理预存 eslint 错误（15→0）并恢复规则为 error，React Compiler 顾问规则与 shadcn 变体导出按文件豁免。

## 0.4.0（2026-06-21）

### Changed
- **监控探针改用 ServerProbe，退役自写插件桥**（FR-010 / ADR-014 取代 ADR-012）：将 [ServerProbe](https://github.com/wcpe/ServerProbe) 作为 git 子模块引入 `third_party/ServerProbe`，CP 经 `go:embed` 内嵌探针 jar，建服 provision 时经新增 gRPC `DeployServerProbe` 自动写入实例 plugins 目录并下发最小 config.yml（仅本机回环开启 `/metrics` + 系统分配 probe 端口，29940 段）；Worker `GetInstanceMetrics` 改为优先抓取 ServerProbe `/metrics` 取 TPS/MSPT/堆/线程/CPU/世界负载等富指标，探针未部署/抓取失败时回退 RCON+RSS。前端实例详情页展示富指标四宫 + 按世界负载表。**同时删除自写 jianmanager-bridge**（Bukkit/BungeeCord 插件源码、Worker `/ws/plugin-bridge`、gRPC `StreamPluginEvents`/`SendPluginCommand`、CP `plugin_bridge` service/router/SSE、前端 PluginBridgePage 与侧栏入口）；玩家治理（踢/封/whitelist）由 FR-054 RCON 路径承担。FR-103/FR-055 标记 deprecated；构建配方 `make embed-probe`（需 JDK21 + 子模块）。真机验证：真 Paper 1.21 + ServerProbe 抓得 TPS=20.03/MSPT=0.53ms/heap 434/1024MB/threads=60/2 worlds

### Added
- **日志持久化、归档与保留**（FR-049）：实例 stdout/stderr 经 StreamInstanceEvents 上报、平台结构化日志经 slog 装饰器，统一异步缓冲批量入库 `logs` 表（采集侧非阻塞）；`GET /logs` 按 source/level/instance/node/keyword/time 分页检索（DB 侧过滤不全量序列化）+ `GET /logs/export` 导出 NDJSON；超保留天数/总量上限的旧日志按 NDJSON 滚动归档到数据根 `var/log` 后清理；保留策略 `log_store` 可配（保留天数/总量上限/巡检周期，均有默认值）；RBAC 组成员仅见有权实例日志、平台日志仅管理员可见。前端日志中心查询页（筛选+分页+导出，i18n zh/en）
- **配置文件管理引擎**（FR-031）：properties/yaml/toml/json/txt 解析回写**保留注释/键顺序**；6 类 MC 配置内置字段 schema；配置编辑器**文本/表单双模式**，表单按 schema 渲染（bool 下拉/选择项/数字/文本）、保存走字段级补丁（properties 行级 / yaml AST / toml 行级，保留注释）；跨实例一致性校验入口（端口唯一/online-mode 配套/forwarding secret 一致）；每次保存生成版本，diff 与一键回滚；读写经 gRPC 委托 Worker。真机复验：真 BungeeCord config.yml + 真 Paper server.properties 表单编辑、注释保留
- **JDK 一键安装下载源可配**（FR-033）：Temurin/Corretto/Zulu 下载基址经 `JIANMANAGER_JDK_<VENDOR>_BASE` 覆盖（默认 Adoptium 等官方源，便于国内镜像）；补齐 FR-033 验收（注册表/绑定/JAVA_HOME+PATH 注入/删除占用拒绝均真机或单测覆盖）
- **MC 群组服关系模型**（FR-032）：实例角色化（proxy/backend/universal）、proxy↔backend M:N 注册（alias/priority/forced-host/restricted）、Network 非独占软标签 + 群组视图批量启停、节点端口占用查看、工作目录系统分配（创建对话框改只读）
- **搭建代理向导**（FR-035）：BungeeCord/Waterfall（PaperMC）+ Velocity（modern 转发，自动生成 forwarding-secret 下发所注册后端 + 跨代理一致校验）；把已有 backend 注册进代理写 servers/priorities/forced-host；可选 online-mode（持久化，离线模式群组服可关闭）。真 Paper 1.20.4 + 真 BungeeCord 端到端复验：玩家经代理进入后端
- **一键复制子服**（FR-036）：复制为独立新实例（系统分配新目录/端口），排除 session.lock/logs/缓存/usercache 等运行态文件，修正端口/rcon/motd/可选 level-name 并保留 forwarding secret，可选注册进 0/1/多代理，复制前 dryRun 预检冲突。端到端复验：克隆独立启动并经代理进入
- **通用文件改前自动备份与版本回滚**（FR-051）：编辑器保存或上传覆盖**已存在**的任意文件前自动生成快照（含二进制文件，base64 无损存储），提供版本列表（时间/大小/操作者）、unified diff（二进制自动识别并跳过文本差异）与一键回滚（回滚前再快照当前内容，回滚本身可再回滚）；版本落 CP DB（事实源在 CP，文件归 Worker，经现有文件读写 gRPC 委托，无新增 proto），保留上限与触发快照大小阈值可配（`file_version.max_per_file` / `file_version.max_size_bytes`）。与 FR-031 配置版本同机制并复用 `unifiedDiff` 等逻辑，刻意分表区分文本/二进制语义；前端文件浏览器加历史版本面板 + diff + 回滚二次确认（i18n zh/en + 主题）
- **插件/模组单服管理**（FR-052）：实例工作区/详情页新增「插件」标签，列出 `plugins/` 与 `mods/` 目录 jar 并识别启用（`*.jar`）/禁用（`*.jar.disabled`）状态；上传先入制品库（`type=plugin`，sha256 去重）再经文件 gRPC 部署到实例；启用/禁用经重命名切换（不删除文件），删除二次确认；复用文件 gRPC 与 AuthzService 实例级隔离，写操作经审计中间件记录
- **实例批量操作**（FR-058）：`POST /instances/batch` 按 id 列表或筛选（节点/状态/角色）选目标，action=command/start/stop/restart/kill；CP 侧信号量分片有界并发（上限 5000、并发 16）经 gRPC 复用既有 per-instance RPC 扇出，无新增 proto；权限隔离下沉 SQL，越权/不存在 id 静默计入 skipped；command 仅对运行中实例下发；返回成功/失败/跳过计数。前端实例列表多选 + 批量操作栏，批量停止/强制关服二次确认（强制关服需输入 FORCE），危险操作经审计中间件留痕（`instance.batch`）
- **节点维护模式与主动下线**（FR-048）：节点新增 `maintenance`（cordon）标记，与在线/离线正交；维护中拒绝新实例调度到该节点（创建期拦截，返回 `NODE_MAINTENANCE`）；排空 `POST /nodes/:id/drain` 停止其上 RUNNING 实例（复用实例停止 gRPC，不迁移）；主动下线 `DELETE /nodes/:id` 解除注册保留记录、复连需重新注册（在线拒绝）；维护/排空/下线写审计（`node.maintenance`/`node.drain`/`node.delete`），前端节点页加维护标记展示与维护/排空/下线操作（排空、下线二次确认）+ zh/en i18n
- **增量备份与备份链恢复**（FR-056）：备份新增全量/增量模式，增量挂最近一次已完成备份为父形成链；Worker 据上次清单按 mtime/size 仅打包变化文件并回传完整文件清单；恢复沿父链回溯解析整链（全量基 + 各增量）按序回放；列表展示模式与链关系；删除被增量依赖的备份予以拒绝
- **备份远程存储**（FR-057）：备份存储位置可配本地/S3 兼容/SFTP/WebDAV，凭证以 `${ENV_VAR}` 引用不落明文（创建时校验拒绝明文）；创建备份可选目标存储，远程备份恢复=拉回本地再回放；与制品库 storage_backend 模型对齐；`GET/POST/DELETE /backup-storages`（平台管理员）。S3 仅用标准库实现 SigV4，无新增第三方依赖
- **危险操作保护体系化**（FR-059）：统一 `DangerConfirm` 组件收敛全部破坏性二次确认——高危操作（删实例/删用户）要求输入资源名逐字校验，角色门禁按范围（组管理员/平台管理员）禁用越权确认并提示（前端 UI 拦截，最终拒绝仍由后端 RBAC 强制）。接入删实例/删用户/删群组/删备份/恢复备份/删 Bot/批量停止·删除 Bot 等现有入口；补齐删备份此前缺失的二次确认；删除被取代的 ConfirmDialog。i18n(zh+en) + 暗/亮色主题

### Fixed
- **RCON 鉴权包类型错误**（FR-054 / FR-022）：RCON 客户端鉴权帧误用类型 2(EXECCOMMAND) 而非 3(SERVERDATA_AUTH)，连接从未鉴权、命令被服务端在鉴权前拒绝却被上报为成功——kick/ban/whitelist 及指标 RCON 形同空操作；改用类型 3 发送鉴权并校验响应 requestID != -1（密码错/被拒时报错）。真机复验 FR-054 发现，修复后真服踢出在线玩家成功，补假 RCON 服务端回归测试
- **运行中实例备份失败**（FR-056）：备份打包未排除 `world/session.lock`，运行中的服务端对其持有独占锁，Windows 上读取报「另一进程锁定文件」导致整次备份失败（0 字节）；改为排除 session.lock/logs/cache/usercache.json/*.pid 等运行态文件（与 FR-036 一键复制一致）。真机复验发现，修复后对运行中真 Paper 打包 186 文件/170MB 成功，补回归测试
- **代理 daemon 停止缺陷**（FR-035 / FR-006）：daemon 优雅停止此前硬编码向 stdin 发 MC `stop`，代理（BungeeCord/Waterfall/Velocity）不认该命令而一直挂到超时才强杀，超时窗口内重启时旧进程仍占监听端口致新进程端口冲突崩溃（`exit status 1`）；改为 CP 按实例角色派生停止命令（后端/通用 `stop`、代理 `end`）经 `CreateInstance.stop_command` 下发并烤进 wrapper 配置（空值回退 `stop`）。并在 daemon 重启前按 PID 文件等待上一代 wrapper/Java 完全退出（`WaitForPriorExit`，`JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT` 可覆盖），消除快速 stop→start 的端口竞态；修复重启复用同一 strategy 时陈旧 reaper 误改新实例状态；修复 daemon `Kill` 在 Windows 上仅杀 wrapper 进程、致 Java 孤儿化继续占监听端口（重启 `Kill`+`Start` 时新进程 `java.net.BindException` 崩溃），改用 `taskkill /T` 终止整棵进程树

### 已知限制
- **备份远程存储（FR-057）live 传输未真机验证**：S3(SigV4)/SFTP/WebDAV 后端经单测覆盖，但真实 MinIO/SFTP/WebDAV 端点的 upload/download/恢复 live 传输尚未真机验证（转 backlog 补齐）。本地备份存储不受影响。

---

## 0.3.0（2026-06-20）

全链路运维打通 + 终端重写 + daemon 健壮性版本。打通「节点 → 实例 → 终端 → Bot 进服」完整运维链路（FR-043），落地一键搭建 Paper 子服向导（FR-034），重写终端体验，并修复一批 daemon 生命周期与控制面健壮性缺陷。

### Added
- **全链路运维打通**（FR-043）：节点在线 → 创建启动真实 MC 实例 → 浏览器终端交互 → Bot 真正进服 → 运维闭环；全链路 e2e 覆盖节点/实例/终端/Bot 进服/停止断开五条验收
- **一键搭建 Paper 子服向导**（FR-034）：选版本/资源，系统分配目录与端口、下载核心、写 eula/server.properties、结构化启动；向导默认绑定节点最高版本已装 JDK
- **接通 Bot 进服 gRPC 链路**：Worker 实现 CreateBot/DeleteBot/SetBotBehavior/SendBotCommand/ListBots 并托管 bot-worker；CP 据 Config 与实例解析连接目标、用 Bot 名作游戏内用户名、经 ListBots 实时回填状态（此前 Worker 侧无实现、dist 为 mock 桩）
- **运维控制台统一**：点击实例名进统一控制台（终端/文件/配置/Bot），工具栏含启动/停止/重启/强制终止
- **终端重写**：上下命令历史 + 右侧历史抽屉；Tab 补全命令、在线玩家名（据输出实时维护）与 `@` 选择器；拖选复制/全选/复制全部/保存日志/右键菜单；停服终端保持连接转只读并续流关服日志
- **文件编辑器语法高亮**（FR-008）：CodeMirror 6 集成，按扩展名 YAML/JSON 语法高亮 + 行号 + 撤销/重做（此前为纯 textarea）

### Fixed
- **daemon 优雅停止看不到停止日志**：停止改为向 stdin 发 MC `stop` 优雅关服、流出完整停止日志（Stopping/Saving worlds…），此前 Windows 一律 `taskkill` 硬杀；超时强杀兜底（`JIANMANAGER_GRACEFUL_STOP_TIMEOUT` 可覆盖）
- **停服后无法再启动**：实时态心跳曾把停止瞬态 STOPPING 覆盖已记账 STOPPED，致 STOPPING→STARTING 被拒；改为仅 RUNNING→CRASHED 纠正
- **daemon 重启 panic**：Start 重置 connectDone，避免 close of closed channel 崩溃整个 Worker
- **崩溃实例永显 RUNNING 不可删**：wrapper 连续快速崩溃放弃自动重启并退出、心跳实时态对账落 CRASHED
- **实例恢复工作目录丢失**：PID 记录持久化 WorkDir，Worker 重启恢复后文件/配置不再 `open` 空路径
- **CP 重启失联**：Heartbeat 重建 gRPC 连接池、对账未恢复实例为 STOPPED，避免 NODE_OFFLINE 与永卡 RUNNING（生命周期 422）
- **Bot 重连/创建**：CP 批量启动补传连接目标、bot-worker 重建已存在连接；创建表单 config JSON 序列化修复 400
- **终端一次性 token TTL**：30s → 10min，修复重开终端复用过期 token 握手失败
- **direct 停止进程树泄漏**：`taskkill /T` 递归终止，避免泄漏 java/node 子进程
- **控制台导航**：点击侧栏导航即关闭实例工作区，修复从实例页打开控制台后点同路由「实例」不关闭
- **daemon 崩溃即时反映**：wrapper 退出区分主动停止与崩溃并即时推送状态，崩溃不再等下次心跳（~30s）才反映到前端

---

## 0.2.0（2026-06-17）

运行时集成补全 + 嵌入前端可用性修复 + i18n 完整化版本。新增实例状态实时推送与模板默认工作目录；修复了单二进制部署下前端无法加载的重定向死循环、前端构建失败与 i18n 文案缺漏。

### Added
- **实例状态实时推送**（FR-018）：StreamInstanceEvents gRPC 流经 Control Plane SSE 代理推送到前端，替代轮询
- **模板默认工作目录**（FR-014）：模板新增 `defaultWorkDir` 字段，从模板创建实例时自动填充工作目录与启动命令
- **InstanceDetailPage 全面 i18n**：实例详情页全部文案接入 i18next
- **Users/Groups/Templates 页面 i18n 化**（FR-016）：三个页面此前完全硬编码中文，接入 i18next 实现中英双语

### Fixed
- **嵌入前端重定向死循环**：单二进制部署时 `c.FileFromFS("index.html")` 触发 `http.FileServer` 的 `/index.html → ./` 规范化 301 跳转，与根路径形成死循环（ERR_TOO_MANY_REDIRECTS），UI 完全无法加载；改为预读 index.html 并以 `c.Data` 直接返回
- **前端 TypeScript 构建失败**：修复 `events.ts`/`AlertsPage`/`BackupsPage`/`UsersPage` 共 8 处类型错误，恢复 `npm run build`
- **i18n 文案缺失**：补齐 `groups` 与 `backups` 命名空间在 en/zh 的缺失键，消除英文模式下的中文泄漏
- **E2E 测试孤儿进程**（FR-028）：`go run` 派生的二进制子进程未随测试结束回收，导致 `go test` 报 `Test I/O incomplete` 退出非零；改用进程树终止（Windows `taskkill /T`）+ `WaitDelay`

---

## 0.1.1（2026-06-17）

Bug 修复 + 前端 UX 标准化版本。修复终端连接闪烁、启动命令引号、文件浏览器 422 等实际使用问题。

### Added
- **前端通知系统**（FR-030）：引入 sonner Toast 通知库，全局 Toaster 组件
  - 实例操作（启动/停止/重启/终止/删除）使用 Toast 反馈
  - 文件操作（保存/上传/删除/重命名）使用 Toast 反馈
  - 创建实例错误使用 Toast 替代内联 error div
- **ConfirmDialog 组件**：可复用的确认对话框，替代所有 `window.confirm()` 调用
  - 实例删除、Bot 删除、用户删除、备份恢复均使用 ConfirmDialog

### Fixed
- **终端连接闪烁**（BUG-002）：Terminal 组件在 token 加载完成前显示 spinner 占位，不再出现 [连接错误]；WebSocket 连接失败自动重试（最多 3 次，间隔递增）
- **控制台与终端 Tab 重复**（BUG-003）：合并为单一「终端」Tab，上方显示实例指标（TPS/在线/内存），下方为可交互终端
- **文件浏览器 422 错误**（BUG-004）：创建实例时 workDir 改为必填；后端在 workDir 为空时返回明确错误信息
- **启动命令多余引号**（BUG-005）：后端 `sanitizeStartCommand()` 去除外层引号包裹；前端添加「不要用引号包裹」提示
- **cmd /s /c 回归**：Go `exec.Command` 的 `EscapeArg` 与 cmd.exe `/s /c` 不兼容，回退为 `cmd /c`

---

## 0.1.0（2026-06-17）

首个正式版本。覆盖 PRD P0 全部 9 条功能需求 + P1/P2 主要能力。

### Added

#### 核心功能 (P0)
- **首次启动引导**（FR-017）：Web UI 引导管理员设置账号，替代配置文件 bootstrap
- **用户认证**（FR-001）：JWT 双 Token（15min access + 7d refresh），bcrypt 密码加密，自动刷新
- **用户与权限**（FR-002）：平台管理员/组管理员/组成员三级角色，RBAC 权限节点中间件
- **用户组与配额**（FR-003）：组 CRUD、成员管理、实例配额检查（实例数/Bot 数/存储）
- **节点注册与心跳**（FR-004）：gRPC 注册/30s 心跳/90s 离线检测，前端实时在线状态
- **实例生命周期**（FR-005）：状态机（STOPPED→STARTING→RUNNING→STOPPING→CRASHED），指数退避自动重启
- **守护进程**（FR-006）：IProcessCommand 策略路由，daemon wrapper 子进程隔离，Unix Socket/Named Pipe 二进制帧协议，PID 文件恢复
- **终端**（FR-007）：xterm.js 浏览器终端，直连 Worker WebSocket，一次性 30s token，多人同时查看，环形缓冲区回放
- **文件管理**（FR-008）：目录浏览 + CodeMirror 在线编辑 + 分块上传/流式下载 + 路径安全检查

#### 重要功能 (P1)
- **Bot 平台**（FR-009）：Mineflayer Bot 管理，行为引擎框架（idle/follow/patrol/guard）
- **监控指标**（FR-010）：gopsutil 采集 + Recharts 仪表盘
- **告警规则**（FR-011）：阈值触发 + Webhook 通知
- **定时任务**（FR-012）：Cron 调度器 + 执行日志
- **备份恢复**（FR-013）：手动创建/列表/恢复

#### 增强功能 (P2)
- **服务端模板**（FR-014）：预设模板 CRUD + 卡片展示
- **审计日志**（FR-015）：中间件自动记录 + 时间筛选查询
- **i18n**（FR-016/020）：i18next 中英文双语，19 个命名空间

#### 前端
- 11 个管理页面 + 实例详情页（6 Tab：终端/文件/设置/监控/备份/Bot）
- 文件浏览器（CodeMirror）+ 终端组件（xterm.js）+ 创建实例对话框

#### 基础设施
- go:embed 前端嵌入，单二进制部署
- gRPC Control Plane ↔ Worker 通信协议
- Docker 部署支持
- API 限流中间件
- RBAC 授权服务（三级角色 + 用户组隔离 + 资源级访问判断）
- E2E 测试框架（testcontainers-go）

### Fixed
- 终端 WebSocket URL 改用请求 Host 构造，修复反代场景连接失败（FR-007/019）
- 备份恢复改为文件级恢复，正确传递实例工作目录给 Worker
- i18n 缺失 key 补齐 + 组件硬编码中文替换为 t() 调用
- daemon PID 清理测试在 Windows 上的 TempDir 竞态修复
