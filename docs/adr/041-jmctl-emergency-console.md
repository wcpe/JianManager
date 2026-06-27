# ADR-041: jmctl 紧急控制台（本机直连守护进程 socket）

- **日期**: 2026-06-27
- **状态**: accepted
- **上下文**: 守护进程 wrapper（ADR-003）即便 Worker Node 重启/崩溃也会保活被托管的游戏服进程，并在本机暴露一个 Unix Socket / Windows 命名管道（二进制帧协议，`internal/worker/daemon`：8 字节帧头 + Stdin/Stdout/Stderr/Control 四通道）。但**只有 Worker 会说这套协议**——一旦 Worker 或 Control Plane 宕掉，运营者就**够不到那台仍在运行的游戏服进程**：看不到它的控制台输出、发不了指令、也没法优雅停它（只能 `kill -9` 硬杀）。需要一个**绕过整个栈、纯本机、依赖极少**的紧急 CLI，直连守护进程做"最后一公里"运维。

## 决策

新增独立轻量二进制 **`cmd/jmctl/`**（控制面/Worker 之外的第三个 Go 入口；bot-worker 是 Node 不计），**只链 `internal/worker/daemon` 帧协议包**（frame + conn + pid_file），不引入 gRPC / DB / Worker service / CP——保持依赖最小、二进制 ~5MB。能力严格被守护进程帧协议**界定**。

### 1. 寻址与发现

- 守护进程为每实例写 `<pidDir>/<uuid>.pid`（`PIDRecord`：wrapper/java PID、`socket_addr`、`work_dir`）与 `<pidDir>/<uuid>.sock`（Win：`\\.\pipe\jianmanager-<uuid>`）。
- `pidDir` 默认取数据根下 pid 目录（ADR-010 FHS，`var/pid`，**以 Worker 实际写入路径为准**，spec 定），`--pid-dir` 标志 / 环境变量可覆盖。
- jmctl 扫 `pidDir` 的 `*.pid` 即发现本机全部受管实例，无需 CP/Worker。

### 2. 命令集（被帧协议界定，详见 `docs/specs/emergency-cli/spec.md`）

- `jmctl list [--pid-dir DIR]` — 列本机全部 daemon：实例 UUID、wrapper/java PID、存活（`IsPIDAlive`）、工作目录、socket（非交互，适合脚本）。
- `jmctl emergency [--instance <uuid 前缀>] [--pid-dir DIR]` — 交互式紧急控制台：无参数时列存活实例供选择，`--instance` 直连。`Dial` socket，Stdout/Stderr Data 帧实时流到终端，键入行作 Stdin Data 帧发入；`Ctrl+C` 发 Control 通道 `stop`（优雅关服）、连按两次发 `kill`（强制终止）；daemon 退出则自动退出。
- `jmctl stop <uuid 前缀>` — 单发优雅停服后退出（镜像 Worker 的 stop_command）。
- `jmctl kill <uuid 前缀>` — 单发强制终止后退出（应急强杀，运营者显式选择）。
- 所有 `<uuid>` 参数支持**唯一前缀补全**（类 docker/git 短 ID）。
- **不提供 `restart`/创建**：jmctl 没有 launch spec（启动命令由 Worker 从结构化配置派生，ADR-008），紧急控制台只做**观察 + 交互 + 停止/强杀**，不做拉起。

### 3. 安全模型

- **纯本机、无网络面**：守护进程 socket 是本机 Unix Socket（文件系统权限保护）/ Windows 命名管道（本机），jmctl 只在**同一宿主**上运行、直接打开该 socket。
- **不做额外鉴权**：能在本机读写该 socket 文件，即已等同拥有宿主级运维权限（足以 `kill` 该进程）——再加 token/JWT 既无必要、也无处校验（CP 可能正宕着）。这是**有意的「绕栈直连 daemon」紧急通道**，不是网络暴露面。
- 架构不变量中「浏览器/网络永不直触守护进程 socket」**不变**：jmctl 仅限本机操作者，不开任何网络端口。

### 4. 打包

- `cmd/jmctl/` 随 Makefile / 发布管线（FR-173）可选交叉编译为 linux/amd64 + windows/amd64；~5MB（仅 daemon 包，无 embed 资产）。本期先落仓库构建目标，纳入 release 矩阵可后续随 FR-173 增量。
- daemon 包须保持**可独立 import**（无沉重传递依赖）——本 ADR 落地时校验其依赖闭包。

## 理由

- **绕栈直连是紧急场景的本质**：故障时恰恰是 Worker/CP 不可用，任何「经 Worker/CP 中转」的方案都用不上；只有直连守护进程才解决「最后一公里」。
- **依赖最小 = 可靠**：只链帧协议包，无 DB/gRPC/网络，编译产物小、启动快、出错面窄，符合"应急工具要简单可靠"。
- **能力被协议界定，不越权**：观察+交互+停止恰是帧协议本就支持的；不做 restart 是因为没有 launch spec，承认边界比硬塞更诚实。
- **本机权限即授权**：socket 的文件系统权限已是天然访问控制，叠加自定义鉴权是多余且在故障态无法校验。

## 后果

- 新增 `cmd/jmctl/`（第三个 Go 入口）；`internal/worker/daemon` 须保持可独立链接（验证依赖闭包，必要时下沉协议为更中立的包）。
- ARCHITECTURE 角色图 + `.claude/rules/architecture-invariants.md` **新增**「jmctl（本机运维 CLI）」作为守护进程 socket 的本机访问方（与 Worker 并列）；同时明确其本机-only、无网络面，不破"浏览器/网络不触 daemon socket"的不变量。
- 完整 spec 见 `docs/specs/emergency-cli/spec.md`（FR-184，**doc-first**：spec 定稿后才写代码）。
- 真机验收：Worker 停掉的前提下，jmctl list 看到在运行实例、emergency 能看输出并发指令、stop 优雅停服 / kill 强制终止。

## 替代方案

- **经 Worker 暴露一个紧急 HTTP/CLI**：Worker 宕了就用不上，与紧急场景前提矛盾；放弃（必须独立于 Worker）。
- **让 Worker 二进制自带 `--emergency` 子命令**：复用同一二进制省一个产物，但 Worker 二进制重（含 gRPC/全套服务），且"应急工具=主程序子命令"在主程序本身故障时心智混乱；放弃（独立轻量二进制更可靠、职责更清）。
- **给 jmctl 加本地 token/口令鉴权**：故障态无 CP 可签发/校验，且本机 socket 权限已是访问控制；放弃（依赖文件系统权限）。
- **支持 restart/创建实例**：需把 Worker 的结构化启动派生逻辑（ADR-008）搬进 jmctl，依赖立刻膨胀、违背"只链帧协议包"；放弃（紧急控制台只观察+停止）。

## 关系

- **ADR-003（守护进程 Wrapper）**：jmctl 是该 wrapper 暴露的本机 socket 的新访问方（应急用），与 wrapper 保活语义正交。
- **ADR-008（结构化启动 + 托管 JDK）**：jmctl 不做 restart，因启动命令派生属 Worker 职责（依赖 launch spec），不在 jmctl 范围。
- **ADR-010（便携数据根）**：jmctl 默认从数据根 FHS（`var/pid`，以 Worker 实际写入路径为准）发现 pid/socket。
- **架构不变量**：守护进程 socket 既有「daemon↔Worker」之外新增「本机 jmctl」访问方；浏览器/网络不触 daemon socket 的约束不变。
- **FR-184（jmctl 紧急控制台）**：本 ADR 的落地 FR；spec 先行。
