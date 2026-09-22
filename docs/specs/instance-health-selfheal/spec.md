# 实例健康巡检与自愈（FR-459）

> 状态：📋 计划　·　关联 PRD：FR-459　·　依赖：FR-455/456（重启韧性与孤儿兜底）、FR-462（动态基线异常检测）　·　关联 ADR：ADR-093（进程生命周期韧性与孤儿治理）、ADR-050（Worker 进程管理器）

## 1. 背景与目标

现平台对「实例是否健康」没有巡检，只有**被动的事后重启**：

- `internal/worker/process/direct.go` 的 `waitLoop`、`internal/worker/process/docker.go` 的 `waitLoop`、`internal/worker/daemon/wrapper.go` 的 `javaWait` 都只在**进程退出之后**触发 `AutoRestart` 的指数退避重启（`backoffDelay`，1s→30s 上限）。
- **无假死检测**：进程仍在，但探针/端口不可达（GC 长停、死锁、World 加载卡死、探针降级）——`waitLoop` 永远等不到退出，实例永远「RUNNING」却不可服务，面板不告警、不重启。
- **无集群级巡检**：60+ 台实例逐台是否健康，运维只能靠手工翻列表；`internal/worker/heartbeat/heartbeat.go` 的 `collectInstanceMetrics` 只在 RUNNING 且 `ProbePort>0` 时抓 ServerProbe `/metrics`，抓取失败仅记日志（FR-411），不构成健康判定，更不触发自愈。
- **无熔断**：崩溃重启无「单位时间重启次数」上限，持续崩溃实例会无限重启，反复占端口/吃资源。

**目标**：新增存活 + 响应双维巡检，识别假死；受控自愈（自动重启卡死实例、崩溃熔断）；策略可配、动作写审计。

**范围内**：Worker 侧巡检与自愈动作 + CP 侧策略配置与审计；复用既有 `ProbePort`/ServerProbe 与 `AutoRestart` 基座。**不做**：不做业务层（如单个玩家掉线）的细粒度健康；不做跨节点自动迁移/重新调度（那是排空与调度的范畴）。

## 2. 设计

### 2.1 存活 + 响应双维巡检

- Worker 侧新增**周期巡检任务**（`manager` 内 goroutine，配置 `worker.health_scan_enabled`/`worker.health_scan_interval`，默认 30s）。
- 对每个在册实例做两维判定：
  - **存活（liveness）**：进程/容器在跑。宿主进程用 `GetPID` + `daemon.IsPIDAlive`；docker 用 `ContainerInspect` 的 `State.Running`。此维与 FR-455/456 的进程侧证据同源。
  - **响应（readiness）**：能对外服务。按类型择一（可配）：
    - TCP 探活：向实例服务端口（MC 的 `server-port`/代理端口）做短超时 connect；
    - HTTP 探活：向 `ProbePort`（ServerProbe `/metrics`）做 `GET`，超时/非 2xx 视为不健康（复用 `metrics.ScrapeServerProbe` 的短超时口径，新增轻量 GET 探针）。
- 判定矩阵：**存活 ∧ 响应** = 健康；**存活 ∧ 不响应** = **假死**（当前缺口的靶心）；**不存活** = 崩溃（交由既有 `waitLoop`/FR-456 孤儿兜底，本 FR 不重复处置）。

### 2.2 受控自愈

- **启动宽限**：实例跨入 `RUNNING`（或崩溃后进入新一轮启动）后的 `StartupWarmup`（默认 5m）内**不做假死判定**。
  MC 的世界加载/模组初始化常达 90s+，若一返回 `RUNNING` 就用响应探测计失败，慢启动实例会被误判假死并在
  `action=restart` 下被反复重启。宽限期内只做存活维判定（真崩溃照常处置）。
- **假死自愈**：连续 `SuspicionThreshold` 次（默认 3）判定假死才动作，避免抖动误重启。策略：
  - `warn`（默认）：仅告警 + 落审计 + 标 `statusReason`（如「假死：进程在但响应探测连续失败 N 次」）；
  - `restart`：走既有优雅重启路径（`Manager.RestartIfRunning` → 复核仍为 `RUNNING` → `stopLocked` 优雅关服 →
    `startLocked`），**不引入新的杀进程路径**；且受 `SelfHealMaxRestarts`（默认 3 次/熔断窗口）次数上限约束，
    达上限后降级为仅告警（落 `health.selfheal_exhausted`），避免假死路径的重启风暴。
- **崩溃熔断**：维护每实例滚动窗口内的崩溃计数（由 `noteProcessCrash`/`emitCrash` 覆盖 direct/docker waitLoop 与
  daemon wrapper 退出事件三条崩溃来源）。判定对**所有在册实例**评估（含 `RUNNING`）：daemon 下 Java 崩溃时
  wrapper 仍存活、Worker 记账恒为 `RUNNING`，判定若只挂在非 `RUNNING` 分支上则永不执行。窗口内崩溃次数超
  `CircuitBreakerThreshold`（默认 5 次/10min）即熔断：停止 `AutoRestart` 并标原因、发站内信告警，等待人工介入。
  - **熔断生效**：direct/docker 由 Manager 侧 `autoRestartAllowed` 守卫拦住自动重启；daemon 的自动重启在 wrapper
    内部按启动期 env 快照完成，Worker 无法直接触及，故经 wrapper 协议下发 **`disable_restart` 控制帧**
    （粘性，仅由对称的 `enable_restart` 复位），wrapper 收到后不再拉起 Java（若此刻确无 Java 在跑则自行收摊退出；
    若 Java 仍在跑则保持托管，待其下次退出即不再拉起）。熔断**持续期间**Worker 每拍巡检幂等补发一次
    `disable_restart`，兜底「熔断瞬间控制连接断开/即时拨号失败」导致的首帧丢失（否则熔断只落记账、wrapper 仍
    在自动重启）。
  - **熔断解除**：仅**人工确认**解除（spec §5），经 Worker 的人工启动入口（CP 操作员 `StartInstance`）显式解除：
    清熔断锁 + 清崩溃窗口 + 恢复 `AutoRestart` + 清健康故障标识，并向仍在托管的 daemon wrapper 下发对称的
    **`enable_restart` 控制帧**（清 wrapper 内粘性开关，恢复自动重启）。**没有该对称帧时，熔断在 Java 仍在运行时
    触发只发禁用帧、保 `RUNNING`，人工解除将只清 Worker 内存账而不复位 wrapper——Java 下次崩溃仍被 wrapper 拒绝
    重启，而 CP/Worker 却认为已解除（假解除、反向 desync）**。若 wrapper 已收摊退出（熔断时确无 Java 在跑的常见
    场景），`enable_restart` 下发会失败但无副作用：该实例已停/重建，下次启动会 spawn 全新 wrapper（`autoRestartOff`
    默认为 false），故解除**不**降级为「仅在实例停止/重建后生效」。熔断态独立记账，**不**借用 `AutoRestart`
    语义（避免任意配置编辑静默清熔断），也**不**随窗口过期自动解除（避免崩溃循环在无人状态下被自动放行）。
  - **熔断态持久化**：`broken` 原因在熔断时落盘为 PID 目录旁的 `<uuid>.circuit` 状态文件，人工解除/实例移除时清除；
    Worker 重启恢复 daemon 实例（`RecoverDaemonInstances`）时读取并恢复熔断态（保持 `AutoRestart=false` 并补发
    `disable_restart`）。动机：daemon wrapper 的粘性 `autoRestartOff` 存活于 wrapper 进程内，Worker 重启不会清除——
    不持久化会让 Worker「忘掉」熔断（以为可自动重启）而 wrapper 仍在拒绝重启，形成反向 desync。
- **动作分级**：所有自愈动作落审计（`internal/controlplane/service/audit.go` 的 `RecordResultSafe`），区分「自动自愈」与「人工操作」的操作者标识。

### 2.3 策略配置与观测

- 策略存 CP 侧（系统设置/实例级覆盖），经心跳响应或既有配置下发通道同步到 Worker；字段：`enabled`、`scanInterval`、`probeKind`（tcp/http）、`suspicionThreshold`、`action`（warn/restart）、`startupWarmup`、`selfHealMaxRestarts`、熔断阈值。
- 巡检结果（每实例健康态 + 不健康原因 + 最近动作）经心跳上报（复用实例状态快照通道，`heartbeat.go`），供 FR-461 健康总览墙与 FR-462 动态基线消费。
- 阈值类信号（响应延迟突升/突降）作为 FR-462 动态基线的输入之一；本 FR 负责**产生与上报**，不重复实现动态基线规则。

## 3. 任务拆分

- [ ] T1 Worker 周期巡检任务骨架 + 存活维度（复用 `GetPID`/`IsPIDAlive`/`ContainerInspect`）（依赖 FR-455/456 的进程侧证据）
- [ ] T2 响应维度探针（TCP/HTTP + `ProbePort` 短超时）+ 判定矩阵（依赖 T1）
- [ ] T3 假死自愈（连续阈值 → `warn`/`restart`，复用 `Manager.Restart`）（依赖 T2）
- [ ] T4 崩溃熔断（窗口重启计数 + 停 `AutoRestart` + 站内信 + 可解除）（依赖 T3）
- [ ] T5 策略模型与下发 + 巡检结果上报 + 审计（依赖 T1~T4、CP 侧配置面）
- [ ] T6 单测覆盖假死判定、阈值抖动、熔断窗口、策略开关（依赖 T1~T5）

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 制造假死（进程在、端口/探针不可达）：连续 N 次后被识别为假死并标原因 | 真机 |
| 2 | 策略 `action=restart` 时假死实例被优雅重启并恢复服务；`warn` 时仅告警 | 真机 |
| 3 | 阈值抖动（偶发一次探测失败）不触发重启 | 单测 |
| 4 | 持续崩溃实例在窗口内重启超限即熔断，`AutoRestart` 停止并告警 | 真机 + 单测 |
| 5 | 熔断移除后自动重启恢复（daemon 经对称 `enable_restart` 帧复位 wrapper）；熔断期间人工启动不受阻 | 真机 + 单测 |
| 6 | 正常退出（stop/crash）不被本巡检误判为假死 | 单测 |
| 7 | 巡检结果经心跳上报，健康总览墙可见 | 真机 |
| 8 | 自愈/熔断动作写审计，标「自动自愈」 | 真机 |
| 9 | 关闭 `health_scan_enabled` 后巡检不产生任何动作 | 单测 |

## 5. 风险 / 待定

- **假死误判**：MC 在世界保存/加载时可能短暂无响应，探针超时设置过短会误判。待定：响应探针超时与 `SuspicionThreshold` 的组合需真机标定（结合 FR-462 基线区分「正常慢」与「真卡」）。
- **探针不可用的兜底**：未部署 ServerProbe 或 `ProbePort=0` 的实例只有 TCP 维度；二进制/beacon 类无「端口」实例需显式配置探针（关联 FR-450 的主动健康检查）。
- **自愈与运维并发**：自动重启须与单实例生命周期锁（`acquireInstanceOperation`）协同，避免与人工 `Start/Stop` 抢锁；`action=restart` 仅走既有 `Manager.Restart` 天然持锁。
- **熔断的解除方式**：自动解除（窗口过期）还是人工确认？首版偏保守——人工确认解除，避免崩溃循环在无人状态下反复触发。
- **与 FR-456 的边界**：本 FR 处置「进程在但假死」，FR-456 处置「孤儿/状态错位」；二者共享进程侧证据来源，需避免对同一实例重复动作（以状态与锁去重）。
