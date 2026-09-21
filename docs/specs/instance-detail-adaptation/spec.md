# 详情界面多形态适配（FR-448 / FR-449 / FR-450）

> 状态：📋 计划　·　关联 PRD：FR-448、FR-449、FR-450　·　依赖：FR-445（能力画像）、FR-447（采集降级编排，仅 FR-449 用到）　·　关联 ADR：[ADR-091](../../adr/091-instance-capability-profile.md)

## 1. 背景与目标

FR-445 提供「实例能力画像」后，详情页需要**据此渲染出四种形态**，并给 BC 与二进制/beacon 两类非 backend 实例补上**它们真正需要的专属内容**（当前这两类打开详情页只有 MC 空壳）。

现状锚点：

- 详情页壳与 Tab 渲染：`apps/control-plane-web/src/components/console/InstanceConsolePage.tsx:41`（`TAB_KEYS`）、`:438`（渲染循环）。
- MC 专有分段：`MetricsSegment.tsx`（探针卡 + TPS）、`ServerStateSegment.tsx`、`BusinessSegment.tsx`、`InstancePlayersSegment.tsx`。
- 现有零散适配：`MetricsSegment.tsx:38` `if (inst?.role === 'proxy') return null`。
- BC 拓扑数据**已存在**：`GET /topology`（`apps/control-plane-web/src/api/topology.ts`，返回 `proxies[].registrations`）与 `lib/topology.ts` 的 `buildTopology`；后端注册关系见 `internal/controlplane/service/registration.go`。

**目标**：

- **FR-448**：详情页按 `role × type × 探针有无` 三轴显隐 Tab 与内容；对非 MC 实例隐藏世界/区块/TPS/插件等 MC 概念。
- **FR-449**：BC（proxy）专属视图——子服列表与各自状态、跨服玩家分布与全网总数、BC 自身运行指标、BC 配置管理。
- **FR-450**：二进制 / beacon 专属视图——进程运行指标、端口 + 主动健康检查、配置文件管理、启动参数编辑；**保留基础文件目录管理与 CPU 等基础监控**，并**抽象到可适配更多产品**。

**范围外**：不改后端采集实现（探针/直探编排归 FR-446/447）；不新增数据真源——BC 视图复用既有注册/拓扑数据。

## 2. 设计

### 2.1 三轴显隐（FR-448）

`InstanceConsolePage` 的可见 Tab 集合由 `useInstanceCapabilities(instance).capabilities`（FR-445）决定，三轴落点：

| 轴 | 来源 | 作用 |
|---|---|---|
| `role` | 实例字段 | 选画像（backend/proxy/beacon/universal） |
| `type` | 实例字段 | 选画像（minecraft_java/generic） |
| **探针有无** | `metrics.probeAvailable` / `serverState.connected` | 同一 Tab 内**两套样式** |

**探针有无两套样式**（同 Tab、不同内容，非隐藏 Tab）：

- `metrics` Tab：有探针 → 走 ServerProbe 全量指标（TPS/MSPT/世界/区块，现有 `MetricsSegment`）；无探针 → 走**轻量进程/直探**视图（FR-450 的 `ProcessPanel` / FR-447 降级后的 SLP 摘要），不显示伪 `-1` 占位。
- `ProbeUpdateCard`：无探针可用或角色不该有探针（proxy/beacon/generic）时整卡隐藏（沿用 `MetricsSegment.tsx:38` 语义，但改由画像 `sources` 驱动，而非 `role==='proxy'` 字面量）。
- **Tab 显隐只由画像 `capabilities` 决定**（画像权威，见 FR-445 §2.3）；**不存在独立的 `mcSemantics` 门控**。`mcSemantics` 语义收窄为「具备 MC 服务端**世界语义**（世界/区块/TPS）」，只决定**同一 Tab 内的字段取舍**——`overview` 对 backend 显示世界/TPS，对 proxy 显示连接数与跨服玩家。故 `(minecraft_java, proxy)` 的 `players`/`plugins`/`bcTopology` 照画像展示（BC 确有跨服玩家与 BungeeCord 插件），而世界/TPS 走 `process` 而非 `metrics`。

### 2.2 FR-449：BC 专用界面（新增 `BcSegment` / `bcTopology` 能力）

新增分段组件 `apps/control-plane-web/src/components/console/BcSegment.tsx`，四块内容：

1. **子服列表 + 各自状态**：直接消费 `useTopology()` 中该 proxy 的 `registrations`（含 `backend.status`/`serverPort`/`enabled`），与注册关系页**共用同一真源**，不重复维护。每行可深链到后端详情。
2. **跨服玩家分布 + 全网总数**：按子服分组展示在线玩家（聚合各 backend 探针 `players` 指标），顶部给全网总数。这是 **proxy 语义下的 `players` 能力**（跨服分布，区别于 backend 的单服实名名单，见 FR-445 §2.3）；玩家来源优先探针（FR-447），后续可接实名。
3. **BC 自身运行指标**：进程级 CPU/内存/线程/运行时长（复用 FR-450 的 `ProcessPanel`，BC 也是 JVM 进程，字段相容）。
4. **BC 配置管理**：`config.yml` 关键项（监听端口、`online_mode`、`servers`/`forced_hosts`）的结构化编辑，走 FR-451 的配置源机制（内联值 / 文件引用二选一）。

> 与既有拓扑/注册页数据一致性：子服列表以 `registrations` 为唯一真源，避免「详情页说的子服」与「拓扑页画的连线」两份数据漂移。

### 2.3 FR-450：二进制 / beacon 专用界面（`process` / `health` / `config` 能力）

新增 `BinarySegment`（承载 `process` + `health`）与 `GenericConfigSegment`（`config`）：

1. **进程运行指标 `ProcessPanel`**：CPU / 内存 / 线程数 / 文件句柄 / 运行时长 / 重启次数。数据来自节点侧进程采集（`internal/worker` 侧已有进程指标通道，`process-metrics` 相关实现）；beacon 无 JVM 堆，展示 RSS 而非 heap。
2. **端口与主动健康检查 `HealthPanel`**：实例声明端口 + 探活方式（HTTP GET 路径 / TCP connect）+ 周期与超时；结果以「可达/不可达/超时」呈现（对齐 FR-447 的三态语义，不报错、不装 `-1`）。
3. **配置文件管理**：复用既有文件管理器（`InstanceResourceSegment` 的 `files` 分段）与 FR-451 的配置文件读写（`config_discover` / `config_read` / `config_write_fields`）。
4. **启动参数编辑**：`startCommand` 明面可编辑（FR-451；`EditInstanceConfigDialog` 已在 `InstancesPage.tsx:803` 存在，迁入详情页）。
5. **保留基础能力**：文件目录管理（`files`）+ 基础 CPU/内存监控（`process` 里的基础档）**必需保留**。

**抽象性要求**：以上分段**不写死 beacon 名**，一律由画像 `capabilities` 驱动；新增一种「原生二进制产品」只需在 FR-445 注册表加一条描述符 + 复用这些通用分段，前端零改动。

## 3. 任务拆分

1. **FR-448**：`InstanceConsolePage` 接入 `useInstanceCapabilities` 过滤 Tab；探针有无两套样式的分流点（`metrics` Tab）。
2. **FR-448**：MC **世界语义**分段（世界/区块/TPS）按 `mcSemantics` 取舍 Tab 内字段（不作 Tab 级门控）；`ProbeUpdateCard` 改由画像 `sources` 驱动。
3. **FR-450**：新增 `ProcessPanel` + `HealthPanel`（`BinarySegment`），端口/探活配置落实例字段或 `launchSpec`。
4. **FR-449**：新增 `BcSegment`（子服列表复用手法同 `TopologyGraph` 的 `buildTopology` 输入）。
5. **FR-451 衔接**：`config` 分段与启动参数编辑接入详情页（依赖 FR-451 的配置源 API）。
6. devmock：补 `capabilities` 与健康检查/进程指标的 mock 响应。

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | FR-448：backend / proxy / beacon / generic 四种实例详情页 Tab 集各不相同且合理 | DOM 测试 |
| 2 | FR-448：切换探针有无，同 Tab 呈现两套内容，无伪 `-1` 垃圾值 | DOM 测试 + 真机 |
| 3 | FR-449：BC 见子服列表/跨服玩家分布+总数/自身指标/config 四块 | 真机过 |
| 4 | FR-449：子服列表与拓扑/注册页数据一致 | 真机过（对照 `/topology`） |
| 5 | FR-450：beacon 与通用二进制呈现进程指标/端口健康/config/启动参数，无 MC Tab | 真机过 |
| 6 | FR-450：文件目录管理与基础 CPU 监控仍可用 | DOM 测试 |
| 7 | FR-450：新增一种二进制产品仅加画像描述符即可复用视图 | 代码审查 |

## 5. 风险 / 待定

- **健康检查执行侧**：HTTP GET/TCP 探活由谁发起（CP 主动探 / Worker 上报）需定；倾向复用 Worker 既有出网能力，避免 CP 直连内网端口。
- **BC 跨服玩家来源**：BC 无 ServerProbe，玩家分布依赖各 backend 探针聚合或后续直探；无探针时降级为「未知」而非 0。
- **`metrics` 双样式的判定时机**：`probeAvailable` 是运行时态、画像 `sources` 是静态声明，需明确「静态声明含 probe 但运行时探针不在」时走降级样式（依赖 FR-447 编排）。
- **FR-451 耦合**：`config` 分段与启动参数编辑依赖 FR-451 落地，本 FR 可先占位后联调。
