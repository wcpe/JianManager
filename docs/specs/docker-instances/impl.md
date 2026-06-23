# 实施计划 — FR-078 Docker 容器化实例运行 + 镜像管理 + 端口映射

> 关联 FR: FR-078 | 优先级: P1 | 状态: 🔨 in-progress | 关联 ADR: ADR-019（依赖 ADR-003/008/010、FR-005/032）

## 背景

`IProcessCommand` 已支持 direct/daemon 两种本机进程启动方式，但 `docker` 一直是占位（`internal/worker/process/docker.go` 全返回 `ErrNotImplemented`）。本 FR 真实现 docker 模式：Worker 经本机 Docker Engine API 管理容器化游戏服，补齐镜像管理与端口映射，并把容器化实例纳入既有状态机/终端/日志/监控/备份通路。

## 设计要点（详见 ADR-019）

- Worker 经本机 Docker 守护进程（`github.com/docker/docker/client`，FromEnv 自动发现）管容器；CP 不直连 Docker，容器/镜像操作经 gRPC 委托 Worker。
- 一个实例 ⇄ 一个容器（命名 `jianmanager-<uuid>`）；`tty=false` + 三路 attach，stdout/stderr 经 `stdcopy` 解复用接终端与日志采集。
- 工作目录（ADR-010 数据根宿主绝对路径）bind-mount 到容器 `/data`；端口经 `PortBindings` 把容器内端口（MC 约定 25565）发布到 FR-032 端口池分配的宿主端口。
- docker 模式不叠 daemon wrapper（隔离由 Docker 守护进程提供）；JDK 随镜像提供，不注入宿主 JAVA_HOME。

## 任务拆解

### 1. ADR 与规格
- [x] 新增 `docs/adr/019-docker-containerized-instances.md`。
- [x] PRD FR-078 状态翻 `🔨 in-progress`。
- [x] 本 spec（impl.md + api.md）。

### 2. dockerStrategy 真实现（worker）
- [x] `go get github.com/docker/docker/client`（主模块 `github.com/docker/docker`），`go mod tidy`。
- [x] `process/docker.go`：create/start/stop/kill/exec 经 Docker SDK；缺镜像自动拉取（`ensureImage`）。
- [x] `CommandSpec` 加 `Image`/`PortMappings`；`Instance` 加同字段；`Manager.SetDockerConfig` 透传；`newStrategy` 路由 docker → `dockerStrategy`。
- [x] 容器输出经 `stdcopy.StdCopy` 解复用路由到 `onOutput`；stdin 经 attach 写入。
- [x] 容器退出经 `ContainerWait` 异步监听，崩溃触发指数退避重启（复用 `backoffDelay`）。
- [x] `GetPID` 经 `ContainerInspect` 取宿主侧容器主进程 PID。
- [x] 注入式 Docker 客户端工厂 + fake 客户端单测（生命周期/端口/输出/停止/PID）。

### 3. 镜像管理（proto + worker + CP）
- [x] `proto/worker.proto`：`CreateInstanceRequest` 加 `image`/`port_mappings`；新增 `ListImages`/`PullImage`/`RemoveImage` RPC 与 `ImageInfo`/`PortMapping` message；protoc 重新生成。
- [x] `process/images.go`：`ListDockerImages`/`PullDockerImage`/`RemoveDockerImage`（可注入客户端，fake 单测）。
- [x] `grpc/docker_ops.go`：3 个镜像 RPC handler，Docker 不可用回报 `docker_available=false`。
- [x] `CreateInstance` 把 `image`/`port_mappings` 经 `SetDockerConfig` 存入实例记账。

### 4. CP 接入（模型 + 服务 + 路由 + 前端）
- [x] `model.Instance` 加 `Image`/`ContainerID`（AutoMigrate 自动建列）。
- [x] `service.CreateInstanceRequest` + router 加 `image`；`registerOnWorker` 下发 `image` 与派生 `port_mappings`（`dockerPortMappings`）。
- [x] `service/docker_image.go` + `router/docker_image.go`：节点级镜像列出/拉取/删除端点；wire 进 router.go + main.go。
- [x] 前端建实例对话框：docker 模式显示镜像输入（必填校验），提交下发 image；i18n 仅加自有键。

### 5. 文档同步与验证
- [x] ARCHITECTURE：进程模型加 docker 策略生命周期、gRPC 加镜像 RPC、instances 表加 image/container_id。
- [x] API.md：POST /instances 加 image、新增镜像管理端点。
- [x] CHANGELOG `[Unreleased]` 追加。
- [ ] 真机：Docker 模式建+启 MC 实例（拉镜像→映射端口→终端见日志→可进服），停/删干净。

## 完成判据

- `go build ./...` 不 panic + `go vet ./...` + 相关 `go test` 绿；前端 tsc/lint/build 绿。
- 真机：Docker 模式建+启 MC 实例跑通（本机 Docker daemon 可用时执行，否则标「待真机验」）。
