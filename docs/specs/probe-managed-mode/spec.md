# 功能规格：探针托管模式协议（权威定义）

> 状态：已交付（上游实现，本次补录文档）　·　关联 PRD：FR-561　·　协议实现：`internal/controlplane/service/managed_mode*.go`
>
> **本文是探针托管模式的协议权威定义。** ServerProbe 仓库的 `docs/specs/managed-mode.md`
> 引用本文而不复制帧定义；两边若出现分歧，以本文 + 探针源码为准。

## 1. 背景与目标

ServerProbe 原本是纯独立产品：行为全由本地 `config.yml` 决定。探针有 66 个配置项分散在
多台服务器上，批量管理与一致性维护成本很高，且改配置必须重启宿主实例。

托管模式给探针增加一个**可选**能力：由外部控制面远程管理探针自身配置，运行期热生效，无需重启。

**厂商中立是硬约束**（不是修饰）：ServerProbe 要能对接多种控制面产品，因此

- 厂商标识只能是**数据**，不得成为代码分支；
- 凭证对探针**不透明**（不解析格式与内部字段）；
- 托管能力属于探针核心能力集，与"谁来驱动"无关。

JianManager 是首个对接方，但协议不假设对接方是 JM。

## 2. 需求（要什么）

- **范围内**：身份文件作为托管开关；配置经既有反向 WS 下行并热应用；配置键三档分级；
  指标经 WS 推送、托管模式下零监听端口；存量部署自动迁移；状态上报。
- **不做**：不管理游戏插件配置；不托管 bootstrap 键（`endpoint`/`instanceId`/`credential`）；
  不实现回滚到更早历史版本（只回退"上一版"，即应用失败时的事务性回退）；
  不删除独立模式的 `/metrics`；不引入第三方依赖（JDK 8 + 零依赖红线）。

## 3. 设计（协议定义）

### 3.1 传输与信封

托管配置复用**既有的插件桥反向 WebSocket**（探针 → Worker 的 `/ws/plugin-bridge`），
不新增端口、不新增端点。

帧统一是 WS 文本帧（JSON），信封：

| 方向 | 形态 |
|---|---|
| 上行 | `{"type":"event","event":"<子类型>","instance":"<uuid>","timestamp":<ms>,...}` |
| 下行 | `{"type":"command","action":"<动作名>",...}` |

**载荷位置的陷阱**：不同 `event` 的载荷放在**不同键**下，两个键名不可互换。

| event | 载荷位置 |
|---|---|
| `config_applied` | `payload` |
| `config_state` | `payload` |
| `metric_snapshot` | `payload` |
| `command_result` | `data` |

下行同理：结构化载荷优先 `payload`（嵌套对象），字符串形态用 `payloadJson`。
探针先取 `payloadJson`、为空才取 `payload`。

> 真实踩坑：CP 曾把 `config_applied` 的**整帧**当载荷解析，于是 `configVersion` 恒为 0、
> `result` 恒为空串——每条回执都被判 failed，且失败原因永远显示"探针未给出失败原因"，
> 看起来像探针的问题。解析必须从 `payload` 段取。

### 3.2 身份文件

身份文件是托管模式的**开关**：文件存在即托管，删除即回退独立模式。

- 落位：探针数据目录下的 `agent-identity.json`，原子写（tmp + fsync + rename）。
- 半写防御：解析失败视为"不存在"，降级独立模式并 WARN，**不崩溃**。
- 探针不解析 `credential` 的内部结构（不透明凭据）。

CP 侧载体：`ManagedModeService.BuildIdentityFile`（`managed_mode.go`）。

### 3.3 配置键三档分级

66 个配置键分三档，规则真源是**探针的 `ConfigTier`**，CP 侧表是它的镜像；
二者偏离时以探针为准。

| 档 | 含义 | 处理 |
|---|---|---|
| `Forbidden` | 开启监听面或承载凭证的键（`bridge.*`/`metrics.*`/`mcp.*`/`web.*`，共 18 个） | **整包拒绝** |
| `Guarded` | 影响数据量/资源/观测覆盖/隐式边界 | 逐键校验物理边界后应用 |
| `Safe` | 仅展示或日志级别，无副作用 | 直接应用 |

**前缀回溯**：`alert.channels.webhook.url` 这类深层路径若祖先已定档，按祖先算。
CP 侧实现 `IsForbiddenConfigPath`（`managed_mode_tiers.go`），与探针 `ConfigTier.isForbiddenPath` 同语义。

**未登记档位按禁止处理**：新键必须先在两端的定档表里收录，否则整包拒绝。
这是有意的 fail-closed——宁可拒绝也不让未审的键进生产。

### 3.4 下行 `config_push`

```json
{
  "type": "command", "action": "config_push",
  "payloadJson": {
    "configVersion": 7,
    "baseVersion": 6,
    "values": { "collect-period-ticks": 40, "aggregation.window": 5 },
    "removals": ["history-capacity"],
    "reason": "调低采集频率",
    "operator": "admin"
  }
}
```

| 字段 | 语义 |
|---|---|
| `configVersion` | 本次目标版本，探针要求**单调递增** |
| `baseVersion` | **乐观并发基线**：探针当前版本必须等于它才应用 |
| `values` | 点分路径键值（`aggregation.window`、`world.sample-period-ticks`） |
| `removals` | 要删除的路径 |

**`baseVersion` 不是可选装饰**：探针要求"当前版本 == baseVersion"才应用，否则整包拒绝并回
`rejected`。CP 必须拿**上一次 `config_applied` 回报的版本**作为基线，不能自增本地计数器——
探针的版本号是它自己的单调序号，与 CP 的代数不是同一回事。首次下发传 0。

**版本双层语义**（最容易混用）：CP 会话的 `configVersion` 与下发的 `baseVersion` 是两个
不同序列，前者是 CP 代数、后者是探针序号。

### 3.5 上行 `config_applied`

```json
{
  "type": "event", "event": "config_applied", "instance": "<uuid>",
  "timestamp": 1794841049442, "dedupKey": "cfg:7:applied",
  "payload": {
    "configVersion": 7, "result": "applied",
    "effective": { "collect-period-ticks": 40 },
    "selfCheck": { "collectorAlive": true, "metricProducible": true, "bridgeUsable": true },
    "error": null,
    "appliedAt": "2026-11-16T14:57:29Z"
  }
}
```

**`result` 只有三种取值**：`applied` / `failed` / `rejected`。

**`conflict` 不是 result 取值**，它是 `rejected` 的 error 前缀（基线不符时探针回
`rejected` 且 error 以 conflict 开头）。判错会让"版本冲突"被当成"协议错误"处理——
两者的运维动作完全不同（前者按最新版本重发，后者查探针）。

**`appliedAt` 是 ISO-8601 UTC 字符串**，不是 epoch 数字。早期 CP 把它声明成 `int64`，
结果每条回执都因 `cannot unmarshal string into ... int64` 整条反序列化失败被丢弃——
矩阵显示"探针没回执"，而探针其实早就应用成功了。CP 现用 `json.RawMessage` 接住原始形态，
ISO 字符串与 epoch 毫秒都认。

**`dedupKey` 形如 `cfg:<version>:<result>`**：同一版本的不同处理结果（先 rejected/failed、
修好后重试 applied）必须是不同去重键，否则按 `(domain, dedupKey)` 幂等落库时会把后到的
成功回执当成重复丢掉，永久停在"被拒"。

### 3.6 上行 `config_state`

探针周期上报的托管状态。

```json
{
  "type": "event", "event": "config_state", "instance": "<uuid>", "timestamp": 1794841078000,
  "payload": {
    "configVersion": 7,
    "effective": { "collect-period-ticks": 40 },
    "cacheAgeSeconds": 29,
    "mode": "managed",
    "connection": "online",
    "capabilities": ["metric_push", "config_push", "config_state"],
    "lastError": null
  }
}
```

两个字段的语义容易读错：

- **`cacheAgeSeconds`**：距上次**成功应用**的秒数；从未应用过时为 `null`（不是 0）。
  null 才是"无基线"的诚实表达，把它当 0 会误判成"刚刚生效过"。
- **`connection`**：探针上行取值为 **`online`** / `degraded` / `unauthorized` / `disconnected`。

> **两套连接态词表**（真实踩坑）：探针上行用 `online`，而 CP 内部合成值用 `connected`——
> 只有"在线"这一项拼写不同。CP 的合成逻辑必须显式归一 `online → connected`，否则该值落进
> fallback 分支：在当下侥幸正确，但白名单残缺（任何新状态都会被静默记成"在线"）。

**`unauthorized` 必须告警而不是重试**：它表示控制面明确拒绝凭据（401/403）。
探针**不会**降级到文件配置（避免"已被托管"的认知分裂），所以等网络恢复也不会自愈，
必须重新签发身份文件。

### 3.7 上行 `metric_snapshot`

托管模式下指标改由 WS 推送，代替 HTTP 拉取 `/metrics`。

```json
{
  "type": "event", "event": "metric_snapshot", "instance": "<uuid>",
  "payload": { "ts": 1794841049, "sequence": 42, "metrics": { "tps": 19.8, "msptAvg": 0.012 } }
}
```

**单位口径（最容易接错）**：`msptAvg` / `msptP95` 是**秒**，不是毫秒。
协议规定毫秒换算在控制面侧做，探针保持与 `/metrics` 的 `serverprobe_mspt_seconds` 同口径。
解析成内部结构时必须 `×1000`，否则面板上的 MSPT 会小一千倍。

**缺项即不写**：不可用字段（TPS/MSPT 为 null、哨兵 -1）探针**直接不输出该键**，不是填 0。
所以所有数值字段在解析侧都必须是指针类型——零值无法区分"真的是 0"与"没给"。

`ts` 是 **epoch 秒**（不是毫秒）。

### 3.8 `hello` 与能力位

连接后首帧，顺序敏感（探针保证其它上行排在它之后）。

```json
{
  "type": "hello", "instance": "<uuid>", "platform": "bukkit", "version": "0.8.0",
  "managed": true,
  "capabilities": ["metric_push", "config_push", "config_state"]
}
```

| 能力位 | 含义 |
|---|---|
| `metric_push` | 指标走 WS 推送；缺该位时 CP 应回退 HTTP 拉取 |
| `config_push` | 接受 `config_push` 热下发 |
| `config_state` | 周期上报 `config_state` |
| `mcp_bridge` | MCP 工具面已挂上桥（零监听下的事故诊断通道）。**只在挂载成功后声明**，故 hello 可能没有而后续 `bridge_status` 有 |

`managed` 是托管模式的硬标识：`true` 表示该探针按身份文件运行，指标走推送、
本地面板/MCP/metrics 三个监听面已关断。缺该字段＝独立模式探针，行为与改造前一致。

**独立模式一个能力位都不声明**：`metric_push` 属托管能力，独立模式既不周期推送，
就不该声明——否则对端会一直等一份永远不会到的推送帧。

### 3.9 应用与回退状态机

```
版本 <= 当前版本 ────────→ rejected(stale)      [不降级]
baseVersion != 当前版本 ─→ rejected(conflict)
含 Forbidden 键 ─────────→ rejected(forbidden)  [整包拒绝]
Guarded 越界 ────────────→ rejected(out_of_range)
校验通过
  ├─ 应用（内存原子替换两代快照）
  ├─ 自检三项（采集器在跑 / 指标可产 / 桥可发帧）
  │   ├─ 全通过 → 原子写 managed-cache → applied(effective)
  │   └─ 任失败 → 回退上一版 → failed(error)   [缓存**不写**]
```

**自检失败时缓存不写**：写下去等于把已知不可用的配置固化成下次启动的真源，比不回退更糟。

**应用必须串行**：命令池是多线程的，而"版本校验 → 替换内存 → 自检 → 落盘"是一段
check-then-act，并发会让两条推送双双通过 `baseVersion` 校验、互相覆盖生效值与版本号。

### 3.10 零监听端口

托管模式下探针关闭 `metrics` / `web` / `mcp` 三个本地监听面，对外暴露面归零。
`/metrics` 的替代是指标推送，web 的替代是控制面渲染，MCP 的替代是 §3.8 的 `mcp_bridge`。

**独立模式保留 `/metrics`**：供非托管场景与兜底。

**失败方向**：托管模式下 WS 成为唯一通道，桥断即无指标——必须显示"指标不可用"
而非静默给旧值。

## 4. 任务拆分

- [x] 身份文件与托管模式状态机（探针侧）
- [x] 配置源抽象与三档分级（探针侧）
- [x] `config_push` 应用、自检、回退（探针侧）
- [x] `config_applied` / `config_state` 上报（探针侧）
- [x] 指标推送与零监听（探针侧）
- [x] CP 侧协议对象与帧构造（`managed_mode*.go`，上游「探针托管协议地基」那次提交）
- [x] CP 侧身份文件生成与下发装配（`probe_managed_assembly.go`）
- [x] **补录本文档与 FR-561 溯源**（本次）

## 5. 验收标准

- [x] 探针能在 Paper 1.8.8 / 1.20.1 / 1.21.1、BungeeCord、Velocity 上完成托管链路
      （ServerProbe 侧 `managed-config*` e2e，7/7 断言通过，四平台 PASS）
- [x] 401/403 拒绝凭据时归入 AUTH_FAILED、不降级为文件配置、恢复后自动重连
      （`managed-auth-reject` 场景）
- [x] 托管模式下探针无任何监听端口（`ss`/`netstat` 核验）
- [x] CP 侧能用真探针跑通下发 → `applied` → 矩阵 `inSync`
      （`internal/e2e/probe_managed_real_test.go`，真 Paper 1.20.1）

## 6. 风险 / 待定

### 协议曾被误实现的三处（均已在 CP 侧修复，实机验证）

1. **载荷位置**：把 `config_applied` 整帧当载荷 → 所有回执判 failed（§3.1）
2. **`appliedAt` 类型**：声明 int64 而探针发 ISO 字符串 → 整条回执被丢弃（§3.5）
3. **连接态词表**：`online` 未识别，落 fallback 侥幸正确但白名单残缺（§3.6）

三者的共同形态是：**协议字段有定义、但从未被真正消费过，所以错误不会显形**。
一旦接上消费链路，第一条真机回执就暴露。

### 待确认

- ServerProbe 侧记录了一处未决项：`metric_snapshot` 的 `dedupKey` 目前是
  `metric:<instance>:<ts>`（秒级），同一秒内"`metrics_now` + 周期推送"会撞键，
  对端按 `(domain, dedupKey)` 幂等时可能丢掉其中一帧。帧内已有单调 `sequence`，
  建议改为带 `sequence`，但需先确认对端去重实现。
- `managed` 字段缺失＝独立模式的判定依赖探针自报；老版本探针不会发该字段，
  按缺省处理是正确的，但这意味着 CP 无法区分"独立模式"与"老版本托管探针"。
