# daemon 子树脱离 Worker 单元 cgroup（FR-500）

> 状态：🔨 开发中（代码 + 回归 + 本地门禁完成，**生产部署与最终验收未做**——本项不含部署动作）
> 关联 PRD：FR-500　·　分支：feat/log-platform-hardening
> 关联 ADR：ADR-003（守护进程 Wrapper 模式）、ADR-093（进程生命周期韧性与孤儿治理）、FR-341（daemon 存活与自动重连，本项是其运行期补强）
> 依赖：无（不改 wrapper 协议、PID 文件、socket 布局与任何对外契约字段）
> 被依赖：无

## 1. 事故与根因（2026-10-01）

**现象**：每次 `systemctl restart jm-prod-worker` 都杀死全部正在运行的 MC 服务器（当日 11 台全灭）。

**生产日志原文**（`~/jm/prod/worker/logs/worker.out`，14:39:29）：

```
INFO 收到退出信号，正在关闭 signal=terminated
INFO wrapper socket 连接断开 instanceId=… error="…: use of closed network connection"
INFO daemon 实例已断开连接（wrapper 继续运行） instanceId=…        ← 11 条，StopAll 的 daemon 分支
INFO wrapper 进程退出 instanceId=… err="signal: terminated"        ← 11 条，wrapper 被 SIGTERM 打死
INFO Worker Node 已停止
```

**根因不在 Worker 代码的关闭路径**，而在 systemd 的 cgroup 终止：

| # | 证据（只读取证） | 结论 |
|---|---|---|
| E1 | `systemctl --user show jm-prod-worker.service -p KillMode -p KillSignal -p SendSIGKILL` → `KillMode=control-group`、`KillSignal=15`、`SendSIGKILL=yes`、`TimeoutStopUSec=90s`；unit 文件 `~/.config/systemd/user/jm-prod-worker.service` **无 `KillMode=` 行** | `systemctl restart` 向该单元 cgroup 内**全部**进程发 SIGTERM |
| E2 | `/proc/<pid>/cgroup`：Worker(3568927) 与 11 个 wrapper(3572286…) 同在 `user.slice/…/app.slice/jm-prod-worker.service`；`ps -o pgid,sid` 显示各 wrapper 已 `setsid`（pgid=sid=自身 pid） | `setsid` 逃得出**进程组**，逃不出 **cgroup**（cgroup 归属不受 session/pgrp 影响） |
| E3 | 生产日志中 `daemon 实例已断开连接（wrapper 继续运行）` 出现在 `wrapper 进程退出 err="signal: terminated"` **之前** | Worker 自己的关闭路径只做断连，**从未**下发停止帧或杀 wrapper；`err="signal: terminated"`（=SIGTERM，非 SIGKILL）与 `KillSignal=15` 精确吻合 |
| E4 | 11 个实例的 **Java 自身日志**在同一秒打出 `14:39:29 [Server thread/INFO]: Stopping server`（Paper 的 SIGTERM shutdown hook；`region1-zone1-lobby` / `login-01` / `region1-zone1-game1` / `region2-zone1-game1` 等逐实例复核一致） | Java 是被 cgroup 级 SIGTERM **直接**打中，不是被「wrapper 死亡」连累——两者同秒、同信号 |

**代码侧核对（当时即正确，本项不修改其语义）**：`apps/worker/main.go:1146 manager.StopAll()` → `internal/worker/process/manager.go` 的 daemon 分支 → `daemonStrategy.Close()`（`internal/worker/process/daemon.go:531`）只关连接；`StopAll` 不调用 `Kill()`（`daemonStrategy.Kill` → `killProcessTree`）——即 spec 与实现的承诺一致：**Worker 关闭路径只断连、不杀实例**。

**既有对策的缺口**：部署层已有 `KillMode=process`（`scripts/install-worker.sh:333`，FR-341 的 K2），但它是**部署期属性**——生产 unit 属早期/手写产物、缺该项，运行期零保障；任何单元漂移（手写 unit、旧版本 unit、非 systemd 托管）都会让「每次部署赔上全部服务器」重演。

## 2. 目标

把「setsid 游离」的承诺在 **cgroup 维度**也落到运行期，使 daemon wrapper 及其 java 子树**不落在 Worker 的 systemd 单元 cgroup 内**，从而与 unit 的 `KillMode` 取值解耦：

1. `systemctl restart <worker 单元>` 后 wrapper 与 java **存活**；
2. Worker 恢复后照旧接管（`RecoverDaemonInstances`）——不改变 PID 文件/socket/协议；
3. 显式 **停止 / 删除 / 重启实例**的语义**不变**（照旧能停）；
4. systemd **用户会话语义不变**：登出 / `stop user@<uid>.service` 仍按 KillMode 回收全部 daemon 子树；
5. 与 FR-455① 接管、FR-497 自动收养、`orphan_scan` 孤儿治理**不冲突**；
6. 任何一步失败（无权限、cgroup v1、只读 cgroupfs、非 systemd 环境）**只告警、保持原状**，不引入新的失败面。

## 3. 设计

### 3.1 迁到哪

目标 = Worker 单元 cgroup 的**同级**目录 `jianmanager-daemons/<实例 UUID>`：

```
/user.slice/user-<uid>.slice/user@<uid>.service/app.slice/jm-prod-worker.service      ← Worker 单元 cgroup
/user.slice/user-<uid>.slice/user@<uid>.service/app.slice/jianmanager-daemons/<uuid>  ← daemon 子树（新家）
```

**为什么是同级而非更外层**：内核要求迁移者对「源与目标的**共同祖先** cgroup 目录」有写权限（`cgroup_procs_write_permission`）。同级目录的共同祖先是 `app.slice`（属该用户、可写）；越靠外层（直到 `/sys/fs/cgroup/` 根，属 root）反而会被 `EACCES` 拒绝。同级也保住了 systemd 用户会话语义（仍在 `user@<uid>.service` 子树内）。

### 3.2 两条迁移路径（缺一不可）

| 路径 | 触发 | 位置 | 覆盖场景 |
|---|---|---|---|
| **wrapper 自迁** | wrapper 进程启动时 | `internal/worker/daemon/wrapper.go::run` 开头（**必须早于 `startJava`**：子进程 fork 时继承父进程当时的 cgroup，先迁后生才能让 java 与 `sh -c` 包裹一并落在隔离目录内） | 新代码 spawn 的 wrapper（此后每次重启都安全） |
| **接管迁移** | Worker 启动接管存活 wrapper 时 | `internal/worker/process/manager.go::isolateAdoptedWrapper`（`RecoverDaemonInstances` 成功重连后调用） | **已在运行**的 wrapper（由旧二进制 spawn，仍留在单元 cgroup）——即「部署新版本后的第一个重启」这一验收场景本身 |

接管迁移按 `/proc/<pid>/stat` 的 PPID 链枚举 wrapper 的**全部后代**（wrapper 与 java 是两棵独立进程组，只迁 wrapper 会漏掉 java），逐个写目标 `cgroup.procs`；单个进程迁移失败不阻断其余。

### 3.3 保守边界与生命周期

- **只在自身 cgroup 确为 systemd service 单元**（末段形如 `*.service`）时生效。手工前台运行（session/scope）、容器内、cgroup v1 一律不动——那里不存在「重启单元连坐」的问题面。
- **逃生开关**：环境变量 `JM_DAEMON_CGROUP_ISOLATION=0|false|no|off` 关闭隔离，默认开启。Worker 用 `os.Environ()` 把环境原样传给 wrapper，故一处设置对 Worker 与全部 wrapper 同时生效（`internal/worker/daemon/cgroup.go`）。
- **回收**：wrapper 正常退出时 `rmdir` 自己的隔离目录（非空则失败并留给兜底）；被 `kill -9` 留下的空壳由 Worker 启动清扫 `SweepEmptyDaemonCgroupDirs()`（`process.NewManager`）清掉——只删**空**目录，绝不动仍在册的实例目录。
- **失败降级**：推导失败/无权限/写失败一律 WARN 并**保持原状**，行为与该修复前完全一致。

### 3.4 与显式停止/删除的正交性

所有「显式终止」路径按**进程组**（`Setpgid` / 负 pgid，`daemon.KillPIDTree`、`process.killProcessTree`）与 PID 终止，与 cgroup 归属无关：迁移只改 cgroup 成员关系、不发任何信号，故 `Stop` / `Kill` / `Delete`（`ReapDaemonForDelete`）在迁出后照旧生效。

### 3.5 Worker 关闭路径契约（正式表述）

- **只断连**：Worker 因信号 / 重启进入关闭序列时，对 daemon 实例**只关闭与 wrapper 的连接**（`Close()`），不下发停止帧、不发信号、不杀进程树；wrapper 与其 java 子树必须存活并可被下一次启动接管。
- **显式例外**：用户显式「停止 / 删除 / 重启实例」仍按既有语义终止进程（停止走控制帧优雅关服 + 超时强杀兜底；删除走 PID/进程组强杀两棵树）；这两类语义与本契约正交，不得因本项而改变。
- **cgroup 边界**：daemon 子树必须位于 Worker 单元 cgroup **之外**（本节 §3.1 的目标目录），使「重启单元」在 cgroup 维度也碰不到它；即使部署层漏配 `KillMode=process` 亦成立。

## 4. 验收

### 4.1 可转红回归

| 用例 | 内容 | 转红条件（实测） |
|---|---|---|
| `internal/worker/process/shutdown_cgroup_test.go::TestDaemonSubtreeSurvivesWorkerUnitCgroupKill` | 真实进程：wrapper（测试二进制的 helper 入口跑生产 `daemon.Run`）经 `sh` 先落进「假 Worker 单元 cgroup」再 exec（wrapper 一出生即在单元 cgroup 内，java 随后 fork 继承）→ 断言 ①wrapper/java 均已在隔离 cgroup、单元 cgroup 无平台进程 ②`Manager.StopAll()`（关闭路径）后两者存活 ③对该单元 cgroup **全员发 SIGTERM**（=KillMode=control-group/KillSignal=15）后两者仍存活 ④新 Manager `RecoverDaemonInstances` 能再次接管（RUNNING） ⑤显式 `Stop` 仍能停掉实例 | 逐字移除 `wrapper.run` 的开头自迁调用后**实测转红**（首条即 `wrapper 应自迁至隔离 cgroup` 失败，末条 `显式 Stop` 之外全部失败），随后逐字还原转绿 |
| `internal/worker/daemon/cgroup_test.go` | 路径推导的纯函数边界（service 单元 / scope / 根 / 相对路径 / 已在隔离目录内 / 无 UUID）、环境开关取值、假挂载点下的自迁写路径与后代枚举、清扫只删空目录、退出清理遇非空目录不动 | 假挂载点注入（`cgroupMountRoot` / `ownCgroupReader`），不触碰真实 cgroup 层级 |

`JM_DAEMON_CGROUP_ISOLATION=0` 亦可用于等价复现旧行为（开关关闭 → 不自迁 → 用例转红）。

### 4.2 真实二进制三臂实验（`.tmp/fix-restart-kills/lab/cgroup-restart-test.sh`）

真实 `apps/worker` 二进制 + 真实 daemon wrapper + 替身 java，全部落进「假 Worker 单元 cgroup」（`<lab scope>/worker.service`），再对该 cgroup 全员发 SIGTERM：

| 臂 | 配置 | 实测结果 |
|---|---|---|
| `red` | 隔离关闭（= 修复前行为） | worker 日志 `收到退出信号` → `daemon 实例已断开连接（wrapper 继续运行）`；**wrapper 与 java 双双 DEAD** —— 与生产现场逐行同款 |
| `fixed-adopt` | wrapper 不自迁（模拟旧代码 spawn），Worker 接管时迁移 | 日志 `已把接管的 daemon 子树迁出 Worker 单元 cgroup … moved=2`；单元 cgroup 只剩 worker；SIGTERM 后 **wrapper/java 存活** |
| `fixed-self` | wrapper 自迁 | 日志 `daemon 子树已迁出 Worker 单元 cgroup`；单元 cgroup 只剩 worker；SIGTERM 后 **wrapper/java 存活** |

### 4.3 门禁

`go build ./...`、`CGO_ENABLED=0 go build ./apps/worker`、`gofmt`（改动面）、`go vet`、`go test ./internal/worker/process/... ./internal/worker/ -count=1`、`go test ./internal/worker/... -count=1`、`go test -race ./internal/worker/process/ ./internal/worker/daemon/ -count=1` 全绿；端到端用例另在真实 cgroup 内以 `-race` 跑通。

## 5. 风险与边界（如实登记）

- **不覆盖 system 级 unit 以非特权用户运行的情形**：目标目录的父级（`/system.slice`）属 root，`mkdir` 会被拒 → 降级为原状（该场景的保障仍依赖 unit 的 `KillMode=process`）。本项的定位是补齐**运行期**纵深，不替代部署层配置。
- **cgroup v1 不支持**：`/proc/self/cgroup` 无 `0::` 行即放弃迁移（保守不动）。
- **不影响**：docker 实例（容器资源限额由 Docker Engine 注入，与本项无关）、`direct` 实例（非 systemd 单元子进程，本项不涉及）、日志 VL 受管子进程（生命周期本就与 Worker 绑定，不迁移）。
- **生产复验未做**：本项不含部署动作；生产 unit 补 `KillMode=process` 属运维侧变更（脚本模板已含该项），另行执行。**首次重启仍会杀掉旧代码 spawn 的 wrapper**——投产后的第一次重启由「接管迁移」在起服后覆盖，故第二次起不再连坐；若要第一次即生效，需在新 Worker 起来并完成接管迁移之后再做重启。
- **无配置键**：为守住改动面（`internal/worker/process/**`、`internal/worker/daemon/**`、`apps/worker/main.go` 的信号/关闭路径），逃生开关用环境变量而非 `worker.yml`；若后续需要配置化，再按仓库既有 `SetDefault` 风格补键。
