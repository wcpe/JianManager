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

- **假死自愈**：连续 `SuspicionThreshold` 次（默认 3）判定假死才动作，避免抖动误重启。策略：
  - `warn`（默认）：仅告警 + 落审计 + 标 `statusReason`（如「假死：进程在但响应探测连续失败 N 次」）；
  - `restart`：走既有优雅重启路径（`Manager.Restart` → `stopLocked` 优雅关服 → `startLocked`），**不引入新的杀进程路径**。
- **崩溃熔断**：维护每实例滚动窗口内的重启计数（复用 `Instance.CrashCount` 与崩溃快照 `emitCrash`，FR-313）。窗口内重启次数超 `CircuitBreakerThreshold`（默认 5 次/10min）即熔断：停止 `AutoRestart`、置 `CRASHED` 并标原因、发站内信告警，等待人工介入（移除熔断锁后恢复自动重启）。
- **动作分级**：所有自愈动作落审计（`internal/controlplane/service/audit.go` 的 `RecordResultSafe`），区分「自动自愈」与「人工操作」的操作者标识。

### 2.3 策略配置与观测

- 策略存 CP 侧（系统设置/实例级覆盖），经心跳响应或既有配置下发通道同步到 Worker；字段：`enabled`、`scanInterval`、`probeKind`（tcp/http）、`suspicionThreshold`、`action`（warn/restart）、熔断阈值。
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
| 5 | 熔断移除后自动重启恢复；熔断期间人工启动不受阻 | 真机 |
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
