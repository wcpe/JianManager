# daemon 子树脱离 Worker 单元 cgroup（FR-500）

> 状态：✅ 已交付（代码 + 回归 + 三臂实验 + 生产实机取证 + **主动重启生产单元实测**，2026-10-27）。三臂实验已重做并带前置守卫（§4.2）；生产实机取证含部署形态漂移的如实登记与 19/19 隔离实读（§4.5）；**主动重启实测见 §4.6**（当前生产单元 `jm-prod-worker.service`，19/19 实例启动时刻与 PID 均未变）。取证口径与执行入口见 `docs/DEPLOY.md` §9.2
>
> **订正记录（2026-10-12）**：本行原写「生产部署与最终验收未做——本项不含部署动作」，与同一 PR 的 PRD FR-500 状态列（称已做）**相互矛盾**。经核实取事实为准：`~/.config/systemd/user/jm-prod-worker.service` 第 9 行确为 `KillMode=process`，且 `jianmanager-worker daemon` 进程在跑、`jianmanager-daemons` 路径有实际引用 ⇒ **PRD 正确，本行陈旧**，故订正为「已做」。
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

### 4.2 真实二进制三臂实验（2026-10-27 重做，含前置守卫与完整读数）

**为什么重做**：2026-10-01 那轮的三臂结论正确，但存在两个问题——① 脚本落在 `.tmp/`（gitignore），外部读者不可复算；② 脚本**存在平凡通过的真缺陷**（见下）。本轮重建二进制（`5f9e5fbf` 的 `apps/worker`，2026-10-27 13:04 构建）重跑三臂，并把读数、脚本修正与取证目录全部落到本文。

执行环境：Linux 6.1.0-53-amd64 / 40 vCPU / 251 GiB / 机械盘 sda（ServeRAID M1210e）；`systemd-run --user --scope` 提供可写 cgroup 子树；实验进程与生产完全隔离（脚本只动 `$LAB/bin/worker`、`/tmp/jmrk` 与本次新建的 lab scope）。

#### 4.2.1 本轮发现并修正的「平凡通过」缺陷（必须先记）

第一版脚本的首个判定是「wrapper 与 java 都不在 ⇒ red PASS」。这个判据**有歧义**：「不在」既可能是「被 SIGTERM 打死」（真复现），也可能是「压根没启动」（假复现）。本轮首次重跑就撞上了后者，且被直接判成 `VERDICT=red PASS`：

| 触发点 | 表象 | 真实原因 |
|---|---|---|
| `$SERVERS` 目录不存在 | wrapper 监听 socket 失败退出 | `listen unix …: bind: no such file or directory` |
| `$SERVERS/$UUID`（work_dir）不存在 | wrapper 启动 java 失败 | 报错是 `fork/exec /usr/bin/sh: no such file or directory`——**看上去像 sh 缺失，实际是 `exec.Cmd.Dir` 的 chdir 失败** |
| 起进程后立刻 `pgrep` 取 worker PID | `WORKER_PID` 为空 | `setsid` 只是把进程扔出去，`exec` 尚未发生，此刻 cmdline 上还看不到 `bin/worker worker.yml` |

修正（脚本已内置）：
1. **新增 `require_alive` 前置守卫**：三个进程在 SIGTERM **之前**必须 PID 非空且 `kill -0` 存活，否则整轮作废（打 `VERDICT=<arm> PRECONDITION-FAIL` 并 `exit 5`），**不得**判成 PASS 或 FAIL。
2. 补齐 `$SERVERS` 与 `$SERVERS/$UUID` 两级目录。
3. worker PID 改为轮询获取（最多 80 × 0.25s）。

这与仓库里「守卫型断言必须先证明守卫本身被绕过」的纪律（见 `CHANGELOG.md` 2026-10-21 条）完全同型：**守卫型断言（「两都不活」）必须先证明进程真的活过**，否则测的是启动失败而不是事故机理。

#### 4.2.2 三臂读数（每臂均在「关闭前 `require_alive` 全绿」前提下）

真二进制 + 真 daemon wrapper + 替身 java，全部落进「假 Worker 单元 cgroup」（`<lab scope>/worker.service`），再对该 cgroup 全员发 SIGTERM：

| 臂 | 配置 | 关闭前单元 cgroup 成员 | SIGTERM 后 | 判定 |
|---|---|---|---|---|
| `red` | 隔离关闭（= 修复前行为） | `[2524344 2524354 2524421]`（wrapper + java + worker 三者同处） | wrapper **DEAD**、java **DEAD** | ✅ PASS（复现生产：cgroup 级 SIGTERM 连坐） |
| `fixed-adopt` | wrapper 不自迁（模拟旧代码 spawn），Worker 接管时迁移 | `[2533445]`（只剩 worker） | wrapper **ALIVE**、java **ALIVE** | ✅ PASS（单元重启不牵连实例） |
| `fixed-self` | wrapper 自迁（新代码 spawn） | `[2544818]`（只剩 worker） | wrapper **ALIVE**、java **ALIVE** | ✅ PASS（单元重启不牵连实例） |

逐臂关键读数（原样摘）：

**red**（13:09:00）：
```
[5] 关闭前快照
  pid=2524421 ALIVE cgroup=…/jm-lab-rk-red3.scope/worker.service cmd=…/bin/worker
  pid=2524344 ALIVE cgroup=…/jm-lab-rk-red3.scope/worker.service cmd=…/bin/worker d
  pid=2524354 ALIVE cgroup=…/jm-lab-rk-red3.scope/worker.service cmd=/bin/sh ./fake-java.sh
  单元 cgroup 成员: [2524344 2524354 2524421]
[6] 模拟 systemctl restart：对该单元 cgroup 全员发 SIGTERM
  已发 SIGTERM -> pid=2524344 / 2524354 / 2524421
[7] 关闭后：pid=2524344 DEAD / pid=2524354 DEAD
     worker 日志：收到退出信号 → daemon 实例已断开连接（wrapper 继续运行）→ Worker Node 已停止
结果：wrapper 存活=no java 存活=no → VERDICT=red PASS
```
注：worker 自己的关闭路径**只断连、不杀实例**（日志逐行同款于生产事故现场），两个实例进程是被 cgroup 级 SIGTERM 直接打死的。

**fixed-adopt**（13:09:27）：
```
worker 日志：已把接管的 daemon 子树迁出 Worker 单元 cgroup（单元重启不再连坐）… moved=2
          → 已恢复 daemon 实例连接 count=1
[5] 关闭前快照
  pid=2533445 ALIVE cgroup=…/jm-lab-rk-fa.scope/worker.service
  pid=2533377 ALIVE cgroup=…/jm-lab-rk-fa.scope/jianmanager-daemons/labrestart-0001-…
  pid=2533388 ALIVE cgroup=…/jm-lab-rk-fa.scope/jianmanager-daemons/labrestart-0001-…
  单元 cgroup 成员: [2533445]
[6] 已发 SIGTERM -> pid=2533445（只有 worker 在单元内）
[7] wrapper ALIVE / java ALIVE → VERDICT=fixed-adopt PASS
```

**fixed-self**（13:09:57）：
```
wrapper 日志：daemon 子树已迁出 Worker 单元 cgroup（重启单元不再连坐）cgroup=…/jm-lab-rk-fs.scope/jianmanager-daemons/labrestart-0001-…
worker 日志：已把接管的 daemon 子树迁出 … moved=2（wrapper 已自迁，worker 二次确认幂等）
[5] 单元 cgroup 成员: [2544818]
[7] wrapper ALIVE / java ALIVE → VERDICT=fixed-self PASS
```

**结构性判据（两臂共用）**：`fixed-*` 臂关闭前，单元 cgroup 内**只剩 worker 一个成员**——这是比「进程还活着」更迂回也更强的一条：它证明迁移真的发生过，而不是「本来就恰好在安全位置」。

#### 4.2.3 复算入口

脚本与 lab 夹具已在 `.tmp/fix-restart-kills/lab/`（`setup.sh` 建环境、`cgroup-restart-test.sh <arm>` 跑单臂）。复算命令：

```sh
bash .tmp/fix-restart-kills/lab/setup.sh                       # 重建 /tmp/jmrk 环境（含 node-identity.json 与 fake-java.sh）
go build -o .tmp/fix-restart-kills/bin/worker ./apps/worker     # 用当前 HEAD 重建二进制
systemd-run --user --scope --collect --unit=jm-lab-rk-red \
  .tmp/fix-restart-kills/lab/cgroup-restart-test.sh red        # 另两臂把 red 换成 fixed-adopt / fixed-self
```

**脚本仍不入库**（含 `pkill -f` 等本机专属清理逻辑，进库会把实验室细节带进产品仓库）。但本文的读数、判据与前置守卫已足以让第三方重建等价实验。

### 4.3 门禁

`go build ./...`、`CGO_ENABLED=0 go build ./apps/worker`、`gofmt`（改动面）、`go vet`、`go test ./internal/worker/process/... ./internal/worker/ -count=1`、`go test ./internal/worker/... -count=1`、`go test -race ./internal/worker/process/ ./internal/worker/daemon/ -count=1` 全绿；端到端用例另在真实 cgroup 内以 `-race` 跑通。

### 4.4 生产实机复验（2026-10-09）

本项自「生产复验未做」转为**已实测**，三条用户可感知的承诺逐条验证：

| 承诺 | 实测动作 | 结果 |
|---|---|---|
| Worker 重启不牵连服务器 | 对运行中的生产单元 `systemctl --user restart jm-worker-node-main.service`（即本项要解决的触发动作） | **19/19 实例 java 全部存活**（修复前该动作全灭）；新 Worker 启动后自动收养全部活进程，并成功迁移 6 个子树至隔离目录；CP 侧确认 19/19 `RUNNING` |
| CP 崩溃不牵连服务器 | 对生产 CP 发 `SIGKILL`（比 SIGTERM 更极端，等价 panic/OOM） | worker 存活，**21/21 实例存活**；CP 重启后反向隧道自动重建、实例状态自动对账 |
| 重启后自动认领 | 承接上两条 | `RecoverDaemonInstances` + FR-497 收养在 Worker 与 CP 两侧均实测生效，无需人工干预 |

部署侧修复（`KillMode=process`）经 drop-in 落地，并实测该 drop-in 对**重新创建**的同名 transient 单元同样生效（探针验证），故非一次性补救；A/B 对照实测 `control-group` 杀 cgroup 内子进程、`process` 不杀。

### 4.5 生产实机复验（2026-10-27，独立取证）

本节是 §4.4 的**独立重做**（不同机器、不同部署形态、只读取证）。之所以要重做：§4.4 的部署形态与本次现场不同，且 PRD 长期登记「形式化验收记录待补」。

#### 4.5.1 部署形态实测（含一处仍需人工处置的漂移）

```
$ systemctl --user show jm-prod-worker.service -p KillMode -p KillSignal -p SendSIGKILL -p Slice -p LoadState -p ActiveState
KillMode=process        ← 与事故后的修复一致
KillSignal=15
SendSIGKILL=yes
Slice=app.slice
LoadState=loaded
ActiveState=inactive    ← ⚠ 在册但**未运行**

$ systemctl --user show vantaloom-runtime.service -p KillMode -p Slice -p ActiveState
KillMode=control-group  ← ⚠ 真正承载 CP/Worker 的单元没有该防护
Slice=app.slice
ActiveState=active

$ systemctl --user show jm-prod-cp.service -p KillMode -p ActiveState
KillMode=control-group
ActiveState=inactive    ← CP 也不在自己的单元里
```

进程与 cgroup 归属（直接读 `/proc`，不是推测）：

```
/proc/2350148/cgroup → …/app.slice/vantaloom-runtime.service   （Worker，comm=jianmanager-wor）
/proc/1984408/cgroup → …/app.slice/vantaloom-runtime.service   （CP，comm=jianmanager-cp）
两个进程的 PPid 均为 782516（用户 systemd 实例）
```

**结论（必须如实登记）**：本机当前生产 CP 与 Worker **不是** systemd 单元的直接主进程——两者由脚本拉起后被 systemd **收养**，落在 `vantaloom-runtime.service`（一个与本产品无关的单元）内部。于是：

- **部署层防护在本机当前形态下失效**：`jm-prod-worker.service` 的 `KillMode=process` 在册但 inactive；真正承载进程的单元是 `control-group`。
- Worker 自己的启动自检**已如实报出这件事**（这是该自检存在的意义）：
  ```json
  {"level":"WARN","msg":"Worker 单元缺少 KillMode=process 防护：重启或停止该单元会向整个 cgroup 发信号，连坐杀掉全部游戏服（wrapper + java）…",
   "unit":"vantaloom-runtime.service","killMode":"control-group"}
  ```
- `~/.config/systemd/user/jm-worker-node-main.service.d/10-killmode.conf` 现为**孤儿 drop-in**（其主体 unit 文件已不存在，`/run/user/<uid>/systemd/transient/` 也为空）。

⇒ 这一形态下，**运行期 cgroup 隔离是唯一的防线**，正好对应本项的设计意图（§2 第 6 条「单元漂移都会让事故重演」）。下面 §4.5.2 就是用真实生产读数验证这条防线确实生效。

#### 4.5.2 运行期隔离实测：19/19 实例全部迁出（真实读数）

隔离根：`/sys/fs/cgroup/user.slice/user-51404.slice/user@51404.service/app.slice/jianmanager-daemons/`

| 检查项 | 命令/读数 | 结果 |
|---|---|---|
| 叶目录数 | `ls …/jianmanager-daemons/ \| wc -l` | **57**（含历史空壳，与 §3.3 「只删空目录」的清扫时机相关） |
| 有成员的目录 | `for d in …/*/; do wc -l < $d/cgroup.procs; done \| sort \| uniq -c` | **19 个目录各恰好 2 个成员**（wrapper + java）——与生产 19 个实例一一对应 |
| 逐进程核对 | `sed 's/^0:://' /proc/2074628/cgroup`<br>`sed 's/^0:://' /proc/2074677/cgroup` | 两者同为 `…/jianmanager-daemons/17b4cbb9-d3f0-48e9-b943-31dd0d869676` |
| 先迁后生 | `/proc/2074677/status` 的 `PPid` | `2074628`（= wrapper）且同 cgroup ⇒ java 在隔离目录内 fork 出来 |
| 进程身份 | `/proc/2074628/comm` / `/proc/2074677/comm` | `jianmanager-wor` / `java` |
| PID 文件佐证 | `data/var/servers/17b4cbb9-….pid` | `{"wrapper_pid":2074628,"java_pid":2074677,"work_dir":"<实例工作目录>","probe_port":29947}`（原始 work_dir 为本机路径，已脱敏） |
| cgroup 存活态 | 19 个叶目录的 `cgroup.events` | 全部 `populated 1` |
| 迁移日志 | `worker-supervised.log` | 19 条 `已把接管的 daemon 子树迁出 Worker 单元 cgroup … moved=2`，随后 `已恢复 daemon 实例连接 count=19`（12:52:22~12:52:23） |
| 反向证据 | 单元 cgroup 成员身份分布 | 该 cgroup 内的 java 全是 Gradle/Kotlin daemon（构建工具），**无 MC 游戏服 java** |

⇒ **接管迁移臂（§3.2 路径二）在生产形态上 19/19 成功**，与 §4.2 的 `fixed-adopt` 臂结论一致。反向证据同样重要：单元 cgroup 内没有任何游戏服 java，说明「连坐面」已真实消除。

#### 4.5.3 本节**没有**做的事（不得外推）

> **2026-10-27 更新：本节列的「主动重启」已由 §4.6 完成**，且当前生产环境已不再是本节描述的漂移形态（CP/Worker 已回到各自的 `jm-prod-*.service`）。下列文字保留为当时的历史登记，不得当作当前事实引用。

- **没有**在本机的生产单元上主动执行 `systemctl --user restart`（用户实例在跑，属不可自行动作）。§4.4 的「主动重启实测」是在 2026-10-09 的另一台机器上做的，不能直接套用到本机形态。
- **没有**验证 CP `SIGKILL` 臂（同上原因）。
- 因此本节的结论严格限定为：「**当前生产形态下，19/19 实例的 cgroup 归属已正确隔离**」——它足以支撑「即使单元漂移且 `KillMode=control-group`，重启也不会连坐实例」这一推理，但不等于「已做过一次真实重启并观测到实例存活」。后者仍需在可停机窗口由人工执行。

### 4.6 主动重启生产单元实测（2026-10-27 18:51，本节即§4.5.3 缺的那一步）

#### 4.6.1 前置：环境已不是漂移形态，且先证隔离有效

§4.5 记录的漂移（CP/Worker 被收养进 `vantaloom-runtime.service`、`jm-prod-worker.service` 在册但 inactive）**已不再存在**。重启前实测：

```
jm-prod-worker.service : KillMode=process  KillSignal=15  SendSIGKILL=yes  Slice=app.slice  ActiveState=active
jm-prod-cp.service     : ActiveState=active
worker pid=2705116  cgroup=…/app.slice/jm-prod-worker.service  PPid=782516(systemd)
```

**重启前先自证隔离有效**（这是本节与§4.4 那次的区别：先证明不会连坐，再动手）：

| 检查 | 结果 |
|---|---|
| `jm-prod-worker.service` 单元 cgroup 成员 | 3 个：`jianmanager-wor` + 2 × `victoria-logs-p`（都是平台自己的）| 
| 隔离叶目录有成员个数 | **19 / 19** |
| 逐进程核对 19 个叶目录下 38 个进程的 `/proc/<pid>/cgroup` | **无一落在 `jm-prod-worker.service`**（零泄漏）|

⇒ 「单元 cgroup 内无游戏服」已成立，此时重启才是有意义的验证而非冒险。

#### 4.6.2 动作与判据

```
systemctl --user restart jm-prod-worker.service
```
→ RC=0；单元于 **1 秒内**重新 `active`。

| 判据 | 期望 | 实测 |
|---|---|---|
| worker 单元恢复 | active | **active（1s）** |
| 19 实例 java PID 不变 | 全不变 | **19/19** |
| 19 实例**启动时刻**不变（证真未重启） | 全不变 | **19/19**（`ps -o lstart=` 逐实例比对比 PID 更有力：PID 可复用，启动时刻不能） |
| 隔离叶目录仍有成员 | 19/19 | **19/19** |
| 重启后单元 cgroup 内无游戏服 | 零 | **零** |

#### 4.6.3 结论与边界

**「生产单元重启不牵连游戏服」已在当前真实生产形态上完成主动重启实测并通过。** 这是 §4.5.3 明确登记为「仍需人工执行」的那一步。

边界（不得外推）：

- 本次是**主动**重启（运维下发），不是崩溃场景；崩溃由 §4.2 三臂的 `red` 臂覆盖。
- **未**测 CP `SIGKILL` 臂（本次只重启 Worker）。CP 单元是 `KillMode=control-group`，其崩溃靠运行期隔离（daemon 已迁出）而非单元配置兜住，该臂仍未实测。
- 仅一次重启，非长稳；未覆盖「重启同时有实例正在被 Stop/Start」的并发场景。

## 5. 风险与边界（如实登记）

- **不覆盖 system 级 unit 以非特权用户运行的情形**：目标目录的父级（`/system.slice`）属 root，`mkdir` 会被拒 → 降级为原状（该场景的保障仍依赖 unit 的 `KillMode=process`）。本项的定位是补齐**运行期**纵深，不替代部署层配置。
  **2026-10-09 实机补充**：该边界被真实触发，且比原描述更严——13 个 wrapper 位于 `/system.slice/ssh.service`（归 root、由 PID 1 收养），与目标（用户 slice）的**共同祖先是 root 的 `/`**，非特权用户在**任何** cgroup 祖先下都没有可写位置，因此**纯路径回退无解**（已实测确认，非推测）。已补的代码侧动作是「零迁移且 wrapper 仍存活时落 `cgroup.isolate_failed` 审计」（`m.pidAlive` 接缝），把「无保护」变成可观测事实，而不是继续尝试无解的路径推导。
- **cgroup v1 不支持**：`/proc/self/cgroup` 无 `0::` 行即放弃迁移（保守不动）。
- **不影响**：docker 实例（容器资源限额由 Docker Engine 注入，与本项无关）、`direct` 实例（非 systemd 单元子进程，本项不涉及）、日志 VL 受管子进程（生命周期本就与 Worker 绑定，不迁移）。
- **生产复验已完成**（原「生产复验未做」条目已由 §4.4 取代）：生产 unit 的 `KillMode=process` 以 drop-in 补齐并实测生效。原条目担心的「首次重启仍会杀掉旧代码 spawn 的 wrapper」在本次实测中**未发生**——该批 wrapper 因处于 `/system.slice/ssh.service`（非 Worker 单元）而不在连坐范围内；对仍留在 Worker 单元 cgroup 内的 wrapper，接管迁移按 §3.2 覆盖。
- **已知残留**：13 个历史孤儿仍停在 `/system.slice/ssh.service`，其安全性来自「不在 Worker 单元内」而非隔离目录，若该 ssh 单元被重启仍会波及；隔离状态目前只落审计/日志，未上行到 CP 面板展示。
- **无配置键**：为守住改动面（`internal/worker/process/**`、`internal/worker/daemon/**`、`apps/worker/main.go` 的信号/关闭路径），逃生开关用环境变量而非 `worker.yml`；若后续需要配置化，再按仓库既有 `SetDefault` 风格补键。
- **2026-10-27 新登记的残留：部署形态漂移**（详见 §4.5.1）：生产 CP 与 Worker 被 systemd 收养进 `vantaloom-runtime.service`（`KillMode=control-group`），`jm-prod-worker.service` 在册但 inactive。本项的运行期隔离在这里是**唯一防线**，且实测 19/19 生效；但这不改变「部署层没有防护」这个事实，需人工将进程重新纳入带 `KillMode=process` 的单元（或修正承载单元的 `KillMode`）。`jm-worker-node-main.service.d/10-killmode.conf` 已是孤儿 drop-in，应清理。
