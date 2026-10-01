# ADR-097: daemon 子树脱离 Worker 单元 cgroup（运行期自持的 cgroup 边界）

- **日期**: 2026-10-01
- **状态**: accepted
- **关联**: FR-500 · 落地并补强 [ADR-003](003-daemon-wrapper.md)（守护进程 Wrapper 模式——`setsid` 游离与 PID 文件基座）/ [ADR-093](093-process-lifecycle-resilience.md)（进程生命周期韧性与孤儿治理——「重启不杀服务器」的硬约束）· 相关 FR-341（daemon 存活与自动重连及其部署层对策 `KillMode=process`）/ FR-455 / FR-497（接管与自动收养——本项不改其语义）

## 上下文

四层模型 `CP → Worker → wrapper（setsid 游离）→ sh -c → java` 的核心承诺是「Worker/CP 重启不牵连服务器」。[ADR-003](003-daemon-wrapper.md) 用 `setsid` 让 wrapper 脱离 Worker 的**进程组**，[ADR-093](093-process-lifecycle-resilience.md) 与 FR-341 在此之上补齐了 SIGPIPE、优雅停机上限与部署层 `KillMode=process`。

**但 2026-10-01 真机证明这条链路仍会整批失守**：每次 `systemctl restart jm-prod-worker` 都杀死全部正在运行的 MC 服务器（当日 11 台全灭）。只读取证给出确定结论：

1. 生产 unit `~/.config/systemd/user/jm-prod-worker.service` **缺 `KillMode=process`**，生效值 `KillMode=control-group` + `KillSignal=15` + `SendSIGKILL=yes`（`systemctl --user show`）。`systemctl restart` 因此向该单元 cgroup 内**全部**进程发 SIGTERM。
2. `/proc/<pid>/cgroup` 显示 Worker 与 11 个 wrapper 同在 `user.slice/…/app.slice/jm-prod-worker.service`；`ps -o pgid,sid` 显示各 wrapper 的 pgid=sid=自身 pid（`setsid` 确实生效）。**结论：`setsid` 逃得出进程组，逃不出 cgroup**——cgroup 归属与 session/pgrp 正交，子进程在 fork 时继承父进程的 cgroup，此后不因 `setsid` 改变。
3. Worker 的关闭路径**当时即正确**：生产日志中 `daemon 实例已断开连接（wrapper 继续运行）`（`Manager.StopAll` 的 daemon 分支只 `Close()`）出现在 `wrapper 进程退出 err="signal: terminated"` **之前**，且信号名是 SIGTERM（=15）而非 SIGKILL（=9），与 `KillSignal` 精确吻合。
4. 11 个实例的 **Java 自身日志**在同一秒打出 `Stopping server`（Paper 的 SIGTERM 钩子），逐实例复核一致——Java 是被 cgroup 级 SIGTERM **直接**打中，不是被「wrapper 死亡」连累。

即：**spec 的承诺是对的，缺口在于该承诺只在部署层（unit 的 `KillMode`）有保障，运行期零保障**——单元一漂移（手写 unit、旧版本 unit、非 systemd 托管），每次部署就赔上全部服务器。

## 决策

1. **daemon 子树在运行期自持 cgroup 边界**：wrapper 启动时把自身迁出 Worker 的 systemd 单元 cgroup，落到其**同级**目录 `…/app.slice/jianmanager-daemons/<实例 UUID>`。迁移必须**早于 spawn 游戏服**——子进程 fork 时继承父进程当时的 cgroup，先迁后生才能让 java（及 `sh -c` 包裹）一并落在隔离目录内。
2. **接管路径同样迁移**：Worker 启动接管**已在运行**的 wrapper 时（`RecoverDaemonInstances` 成功重连后），按 `/proc/<pid>/stat` 的 PPID 链枚举其**全部后代**（wrapper 与 java 是两棵独立进程组，只迁 wrapper 会漏掉 java）并一并迁移。这条覆盖「部署新版本后的第一个重启」——旧代码 spawn 的 wrapper 仍留在单元 cgroup 内，只有接管方能把它们救出来。
3. **同级而非更外层**：内核要求迁移者对源与目标的**共同祖先** cgroup 目录有写权限；同级目录的共同祖先是属用户可写的 `app.slice`，越靠外层（直到 root 拥有的 `/sys/fs/cgroup/` 根）反被 `EACCES` 拒绝。同级也保住 systemd **用户会话语义**：登出 / `stop user@<uid>.service` 仍按 KillMode 回收全部 daemon 子树（cgroup 边界只对「单元」放宽，不改变会话生命周期）。
4. **保守边界 + 失败降级**：仅在自身 cgroup 确为 systemd `*.service` 单元时生效；手工前台运行（session/scope）、容器内、cgroup v1 一律不动（那里不存在「单元连坐」的问题面，且推导规则不同）。任一环节失败（无权限、只读 cgroupfs、无 `0::` 行）**只告警、保持原状**，行为与修复前完全一致。运行期逃生开关 `JM_DAEMON_CGROUP_ISOLATION=0`。
5. **契约化**：Worker 关闭路径「只断连、不杀实例」与显式停止/删除的例外关系、以及本 cgroup 边界，写入 `docs/specs/daemon-cgroup-isolation/spec.md` 与 `docs/specs/restart-resilience/spec.md` §7，作为后续评审的判据。
6. **不改**：wrapper 协议、PID 文件/socket 布局、任何对外契约字段；显式停止/删除/重启实例的语义（按进程组与 PID 终止，与 cgroup 归属无关）。

## 理由

- **部署期配置不能承担运行期承诺**：`KillMode=process` 是正确且应保留的部署层对策，但它对「已被部署出来的 unit」无追溯力，实测生产 unit 就是缺该项。硬约束（重启不影响服务器）必须在代码里自持，才能在单元漂移下依然成立。
- **`setsid` 与 cgroup 是两个维度**：前者是「谁给我发信号」的进程组/会话问题，后者是「systemd 按什么枚举我」的问题。ADR-003 只解决了前者，本 ADR 补上后者——两者都到位，「游离」才名副其实。
- **为什么不用 `systemd-run --scope` 等外部机制**：那会把 wrapper 的 PID 记账（PID 文件写的是 wrapper 自身 PID，`KillPIDTree`/强杀路径依赖它）与 `systemd-run` 的中间进程语义纠缠在一起，且引入对 `systemd-run` 二进制的依赖；自迁只改 cgroup 成员关系，对既有 PID/socket/接管机制零影响。

## 后果

- **正面**：`systemctl restart <worker 单元>` 在 cgroup 维度也碰不到 daemon 子树，wrapper 与 java 存活并由下一次启动接管；该保障与 unit 的 `KillMode` 取值解耦（配置正确时是纵深防御，配置漂移时是唯一防线）。
- **代价与边界**：新增一处对 cgroup 层级的写操作（隔离目录 `jianmanager-daemons/<uuid>`），需回收——wrapper 正常退出自清，被 `kill -9` 留下的空壳由 Worker 启动清扫（只删空目录）。**不覆盖** cgroup v1 与「system 级 unit 以非特权用户运行」（父目录属 root，降级为原状，其保障仍依赖 unit 的 `KillMode=process`）。
- **可转红防护**：真实进程端到端用例（`internal/worker/process/shutdown_cgroup_test.go`）与真实二进制三臂实验共同钉住该行为；逐字移除自迁调用即转红（见 spec §4）。
