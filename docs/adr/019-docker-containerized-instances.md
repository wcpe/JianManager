# ADR-019: Docker 容器化实例运行

- **日期**: 2026-06-23
- **状态**: accepted
- **上下文**: `IProcessCommand` 已支持 direct/daemon 两种本机进程启动方式（ADR-003），但 `docker` 一直是占位（`internal/worker/process/docker.go` 全返回 `ErrNotImplemented`）。运营者希望把游戏服跑进 Docker 容器以获得：镜像化的运行环境（无需在宿主预装 JDK、依赖随镜像分发）、文件系统隔离、以及后续的资源限额（FR-079，本 ADR 为其铺路）。需要确定 Worker 如何管容器、容器与现有端口池/工作目录/终端/监控/备份如何衔接，且不破坏三进程模型与架构不变量。

## 决策

新增 docker 作为第三种实例启动方式：**Worker Node 经本机 Docker Engine API（`github.com/docker/docker/client`）管理本机容器**，作为 `IProcessCommand` 的 `dockerStrategy` 实现，与 direct/daemon 平级路由。

1. **谁管容器**：仅 Worker Node 经本机 Docker 守护进程（默认 `unix:///var/run/docker.sock` / Windows npipe，由 SDK `FromEnv` 自动发现，可经 `DOCKER_HOST` 覆盖）管理本机容器。Control Plane 不直接连 Docker，所有容器/镜像操作经既有 gRPC 委托给目标节点的 Worker（守 ADR-002 与「CP 不直接操作游戏服进程」不变量）。
2. **容器模型**：一个实例 ⇄ 一个容器，容器命名 `jianmanager-<instance-uuid>`（便于排障与孤儿回收，且天然防重名）。容器以 `tty=false` + `stdin=true` + `attach stdin/stdout/stderr` 创建，stdout/stderr 经多路解复用流接现有终端与日志采集（FR-049），stdin 接终端输入与优雅停止命令。容器策略不走 daemon wrapper（容器进程的生命周期由 Docker 守护进程托管，Docker 守护进程本身即「平台重启不杀容器」的隔离层，无需再叠一层 wrapper）。
3. **镜像模型**：建实例时选镜像（`image` 字段，如 `itzg/minecraft-server:latest`），默认 Docker Hub，registry 经镜像名前缀或 `DOCKER_REGISTRY_MIRROR` 配置。Worker 暴露镜像管理 RPC：拉取（`PullImage`）/列出（`ListImages`）/删除（`RemoveImage`）本机镜像。启动容器前若本地缺镜像则自动拉取。
4. **端口模型**：复用 FR-032 端口池分配的宿主端口（`ServerPort`/`QueryPort`/`ProbePort`），通过 Docker `PortBindings` 把容器内端口（默认与宿主同号，可由镜像约定覆盖，如 MC 容器内固定 25565）发布到宿主。**不引入新网络面**：容器仅向宿主 publish 端口，复用既有端口池的同节点唯一性保证，浏览器/玩家访问路径与 direct/daemon 完全一致。容器 stdio 不经网络，走 Docker attach。
5. **工作目录 / 数据卷**：把系统分配的实例工作目录（`var/servers/<slug>-<shortid>` 的宿主绝对路径，ADR-010）bind-mount 进容器固定挂载点（`/data`），使容器化实例的文件管理（FR-008）、备份（卷挂载即宿主目录，备份逻辑零改动）、配置编辑（FR-021）与 direct/daemon 走同一套宿主侧路径。
6. **资源 / 监控**：本 ADR 只落地运行与端口/镜像；CPU/内存限额留给 FR-079（HostConfig 注入 `--cpus`/`--memory`）。监控沿用 ServerProbe（探针随镜像或经卷注入，端口同 ProbePort），CP 抓取路径不变。
7. **状态机**：容器化实例纳入既有 STOPPED→STARTING→RUNNING→STOPPING→STOPPED/CRASHED 状态机。容器退出由 SDK `ContainerWait` 异步监听，非正常退出回写 CRASHED 并触发既有指数退避重启（与 direct 策略一致，统一在 Manager 层记账）。

## 理由

- **守架构边界**：Worker 只连本机 Docker 守护进程，等价于「Worker 管本机进程」的能力扩展，不新增对外网络监听、不让 CP 触碰容器运行时，三进程模型与依赖方向不变。
- **复用而非另起**：端口走 FR-032 端口池、工作目录走 ADR-010 数据根、终端/日志/备份/监控全部复用既有宿主侧通路，docker 模式只替换「进程如何被拉起」这一层，最小化对上层的侵入。
- **官方 SDK 而非 shell out docker CLI**：`docker/client` 是稳定的 Engine API 客户端，避免解析 `docker` CLI 文本输出的脆弱性，attach 多路流、ContainerWait 等都有一等支持；且不要求宿主装 `docker` CLI（只需守护进程可达）。
- **不叠 daemon wrapper**：Docker 守护进程已提供进程隔离与重启存活，再套 wrapper 纯属冗余；Worker 重启后经容器名/标签重新发现已运行容器即可恢复管理（容器在 Docker 侧持续运行）。

## 后果

- 新增运行时依赖 `github.com/docker/docker/client`（及其传递依赖）；docker 模式要求目标节点宿主已装并运行 Docker 守护进程，未装时该模式的创建/启动返回明确错误（不影响 direct/daemon）。
- 容器内端口与宿主端口可不同号（容器内固定、宿主由端口池分配），端口映射关系需随实例持久化以便展示与排障。
- Worker 重启后需经容器名/标签重新 attach 恢复对已运行容器的管理（恢复路径较 daemon 的 PID 文件更简单：以 Docker 守护进程为单一事实源）。
- 镜像拉取可能耗时较长，拉取作为启动前置步骤需有进度/超时处理。

## 替代方案

- **shell out `docker` CLI**：解析文本输出脆弱、attach/wait 难做、要求装 CLI；放弃。
- **docker-compose 编排**：引入额外 compose 文件与状态，和现有「实例=单进程」模型不匹配，超出单实例运行所需；放弃。
- **Worker 暴露 Docker socket 给 CP 直连**：违反「CP 不直接操作游戏服进程 / gRPC 唯一 RPC」不变量；放弃。
- **特权 sidecar / DinD**：复杂度与攻击面过高，本 FR 不需要；放弃。

## 关系

- **ADR-003（守护进程 Wrapper）**：docker 模式是 ADR-003 中预留但未落地的第三种 `IProcessCommand`；本 ADR 落地它，且明确 docker 模式**不**走 daemon wrapper（隔离由 Docker 守护进程提供）。ADR-003 不被取代，二者为并列的启动策略。
- **ADR-008（结构化启动 + 托管多 JDK）**：docker 模式下 JDK 随镜像提供，结构化启动派生的 java 命令在容器内执行或由镜像 entrypoint 接管；JDK 注入（JAVA_HOME/PATH）对 docker 模式不适用（宿主 JDK 与容器隔离）。
- **ADR-010（便携数据根）**：容器工作目录经 bind-mount 复用数据根分配的实例目录，保证文件/备份的宿主侧一致性。
- **FR-079（实例级资源限额）**：依赖本 ADR，在 HostConfig 注入 cgroup 限额。
