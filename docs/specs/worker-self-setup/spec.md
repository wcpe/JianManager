# FR-222: Worker 免配置自启 setup

- **状态**: 🔨 开发中
- **优先级**: P1
- **关联 ADR**: ADR-051（本 FR 立，改写 ADR-020 的「一键单脚本下载+配置+注册+起」编排立场）；沿用 ADR-020（enrollment token 准入 / 身份持久化 / metadata 传递）、ADR-039（UUID 锚定注册）、ADR-010（数据根 FHS）、ADR-002（gRPC 唯一 RPC 通路）
- **依赖**: FR-224（配置文件 `.yml` 约定，已落地：`worker.yml` 主、`.yaml` 兼容回退）

## 1. 目的

让「节点上线」丝滑化（参考 GitHub Actions Runner 的「下载 / 上线」分离）：把「**下载**（取 Worker 二进制）」与「**上线**（写配置 + 注册 + run）」解耦——下载归外部步骤（安装脚本 FR-223 / 手动拷贝），**上线归 Worker 自己**。

Worker 启动入口自检：**若未配置就进入 setup**（不是分离的 `configure` / `run` 子命令，而是 `run` 入口前置一道自检）。setup 完成「问/取配置 → 写 `worker.yml` → 携 enrollment token 首注册换身份 → 持久化身份 → **转入正常 run**」一条龙，无需任何外部脚本预写配置。

这取代 ADR-020「平台分发单脚本一把梭下载+写 yml+注册+起」中「**脚本写配置**」那一段：新机器只需有 Worker 二进制本身，零脚本依赖即可上线（脚本退化为「取二进制 + 调 Worker setup」，由 FR-223 落地）。

## 2. 设计

### 2.1 未配置判定（进入 setup 的闸）

`run` 入口在加载配置前先判定「是否已配置」：

```
未配置 ⇔ (无 worker.yml 配置文件，含 .yaml 回退) 且 (无 <data-dir>/etc/node-identity.json)
```

- **两者皆缺** → 视为全新机器，进入 setup。
- **任一存在** → 视为已配置（有 `worker.yml` 说明已写过配置；有 `node-identity.json` 说明已注册过身份）→ 跳过 setup，直接走现有 run 流程（保持现状，零行为变化）。
- **配置文件探测**：复用 `config.FindConfigFile`（`.yml` 优先、`.yaml` 回退；搜索 `.` 与 `configs/`，FR-224 既有逻辑）。命令行显式传了配置文件路径（`worker /path/worker.yml`）也算「已配置」（用户显式给了配置，不该再 setup）。
- **身份文件探测**：身份文件路径 `= <data-dir>/etc/node-identity.json`；`<data-dir>` 按与 `dataroot.Resolve` 一致的优先级解析（显式 `--data-dir` > 环境变量 `JIANMANAGER_DATA_DIR` > 默认 `./data`），**只解析路径不创建目录**（避免自检副作用建一堆目录）。

> 判定刻意「任一存在即已配置」（而非「两者皆备才已配置」）：身份文件在、yml 没了，是「配置被删但已注册过」的运维场景，应走重注册（现有 run 路径据身份文件重注册），不该重新 setup 覆盖。

### 2.2 setup 双形态：交互式（TTY）与无人值守（无 TTY）

setup 先探测标准输入是否为 TTY（`golang.org/x/term.IsTerminal(int(os.Stdin.Fd()))`），分两条路径采集入参：

#### A. 有 TTY（交互式）

逐项提示，给默认值，回车接受默认：

| 提示项 | 必填 | 默认 | 说明 |
|---|---|---|---|
| Control Plane gRPC 地址 | 是 | `localhost:9100` | host:port |
| Enrollment token | 是 | 无 | 面板「添加节点」生成的 `jmet_...`；空则重问（必填） |
| 节点名 | 否 | 空（CP 据上报名/预设名生效） | 留空交由 CP/token 预设名 |
| gRPC 端口 | 否 | `9101` | 供 CP 反向连接 |
| WS 端口 | 否 | `9102` | 浏览器终端 |
| data_dir | 否 | 空（即 `./data`） | 数据根 |

- token 必填：空输入则提示并重问（不静默用空 token 注册）。
- 端口非法（非数字 / 越界）→ 提示并重问。
- EOF（管道中途断流）→ 当作无法继续，明确报错退出（不死循环）。

#### B. 无 TTY（CI / 管道 / systemd / Windows 服务）

**不阻塞等待输入**，从命令行参数 + 环境变量读，缺必填项立即明确报错退出（非交互环境卡住等输入是死锁）：

| 入参 | 命令行参数 | 环境变量 | 必填 |
|---|---|---|---|
| CP gRPC 地址 | `--control-plane <addr>` | `JIANMANAGER_CONTROL_PLANE` / `JIANMANAGER_CONTROL_PLANE_GRPC` | 是 |
| Enrollment token | `--token <jmet_...>` | `JIANMANAGER_ENROLL_TOKEN` | 是 |
| 节点名 | `--name <node>` | `JIANMANAGER_NODE_NAME` | 否 |
| gRPC 端口 | `--grpc-port <p>` | `JIANMANAGER_GRPC_PORT` | 否（默认 9101） |
| WS 端口 | `--ws-port <p>` | `JIANMANAGER_WS_PORT` | 否（默认 9102） |
| data_dir | `--data-dir <dir>` | `JIANMANAGER_DATA_DIR` | 否（默认 `./data`） |

- 优先级：命令行参数 > 环境变量 > 默认。
- 缺 CP 地址或 token → `stderr` 打印明确错误（指明缺哪个、怎么补：面板生成 token、或设环境变量）+ 非零退出。
- token 仅用于本次注册，**不写入 `worker.yml`**（见 2.3）。

> 命令行第 1 个位置参数若是 `.yml`/`.yaml`/含路径分隔符的现存文件，仍按「显式配置文件路径」处理（既有行为），不与 setup 的 flag 混淆；setup 的 flag 一律 `--xxx` 具名。

### 2.3 setup 产物与编排

采集到入参后，setup 顺序执行（任一步失败即明确报错退出，不留半截状态）：

1. **写 `worker.yml`**（落在工作目录，与安装脚本 `install-worker.sh` 复刻同一组字段）：
   ```yaml
   # 由 worker setup 生成（FR-222）。enrollment token 不写入本文件（一次性凭据不留盘）。
   name: <节点名 或 node-<hostname>>
   control_plane: <cp-grpc>
   data_dir: <data-dir，仅当显式给出>
   grpc:
     port: <grpc-port>
   ws:
     port: <ws-port>
   log:
     level: info
     format: json
   ```
   - **enrollment token 绝不写入**（与 ADR-020 §2.3 一致；token 短命、用后即焚）。
   - data_dir 仅在显式给出时写（缺省留空 = `./data`，避免把派生的绝对路径钉死进 yml）。
   - 原子写（临时文件 + rename），避免写一半崩溃留坏文件。
   - 若目标 `worker.yml` 已存在（理论上自检已排除，防御性）→ 不覆盖、报错退出。

2. **携 enrollment token 完成首注册**（复用 `internal/worker/register`，走 gRPC，token 经 metadata header `enroll-token`，**不改 proto**）：调用现有 `register.RegisterWithRetry`，CP 校验 token（存在+未过期+未消费+未吊销）后换发 `node_uuid`/`node_secret`。注册沿用现有指数退避重试（CP 暂不可达不立即失败）。

3. **持久化身份**：把换得的 `node_uuid`/`node_secret`/`node_name` 写 `<data-dir>/etc/node-identity.json`（复用 `register.SaveIdentity`，0600 原子写，含敏感 secret 不入日志）。

4. **转入正常 run**：setup 不退出进程，而是把采集到的配置交给既有 run 主体继续（启动 gRPC/WS 服务、心跳等）。实现上 setup 直接复用刚写出的配置在内存中构造 `*config.Config`（避免「写文件再重读」的竞态与多余 IO），并把首注册结果（身份）交给 run 主体，run 主体识别「身份已由 setup 持久化」则跳过重复注册，直接以该身份起心跳与服务。

> **架构不变量守恒**：注册唯一走 gRPC（ADR-002）；token 不落 `worker.yml`（ADR-020）；Worker 不直连 DB（注册/身份均经 gRPC + 本地文件）；身份文件 0600（ADR-020 §3）。

### 2.4 与既有 run 流程的关系

- **已配置**（自检判「已配置」）→ 完全走现有 `runWorker`：`config.Load` → `register.LoadIdentity`（有身份重注册 / 无身份且有 yml 配 token 则首注册，**现状不变**）。
- **未配置** → 先 setup（采集 + 写 yml + 首注册 + 持久化身份），再以 setup 的产物进入 run 主体。
- setup 与既有安装脚本 `install-worker.sh`/`.ps1` **并存**：脚本仍可预写 yml 后启 Worker（Worker 自检判「已配置」直接 run）；脚本退化为「取二进制 + 调 setup」由 FR-223 做，本 FR 不动脚本。

## 3. 接口（Gate-API：命令行参数 + worker.yml 字段）

本 FR 不新增 HTTP/gRPC endpoint（注册复用既有 gRPC `Register`，token 经 metadata，不改 proto）。「接口」体现为 **Worker 命令行参数 + 环境变量 + 写出的 `worker.yml` 字段**：

### 3.1 Worker 命令行（setup 形态）

```
worker [配置文件路径]                # 既有：显式配置文件 → 直接 run
worker                               # 未配置 → 自动 setup（TTY 交互 / 无 TTY 报错指引）
worker --control-plane <addr> --token <jmet_...> [--name N] [--grpc-port P] [--ws-port P] [--data-dir D]
                                     # 未配置 + 无 TTY（或显式带 flag）→ setup 用这些入参，不交互
worker daemon                        # 既有：daemon wrapper 子进程（不变）
```

| 参数 | 类型 | 默认 | 关联 env | 说明 |
|---|---|---|---|---|
| `--control-plane` | string | `localhost:9100` | `JIANMANAGER_CONTROL_PLANE(_GRPC)` | CP gRPC 地址（setup 必填） |
| `--token` | string | 无 | `JIANMANAGER_ENROLL_TOKEN` | enrollment token（setup 必填，不落盘） |
| `--name` | string | 空 | `JIANMANAGER_NODE_NAME` | 节点名（可空） |
| `--grpc-port` | int | `9101` | `JIANMANAGER_GRPC_PORT` | gRPC 端口 |
| `--ws-port` | int | `9102` | `JIANMANAGER_WS_PORT` | WS 端口 |
| `--data-dir` | string | 空（`./data`） | `JIANMANAGER_DATA_DIR` | 数据根 |

> 退出码：setup 缺必填项（无 TTY）/ 写 yml 失败 / 注册失败 / 持久化身份失败 → 非零退出并 `stderr` 打印可操作错误。

### 3.2 worker.yml 字段（setup 写出，与 install-worker.sh 复刻一致）

| 字段 | 来源 | 说明 |
|---|---|---|
| `name` | `--name` 或 `node-<hostname>` | 节点名 |
| `control_plane` | `--control-plane` | CP gRPC host:port |
| `data_dir` | `--data-dir`（显式时） | 数据根；缺省不写（= `./data`） |
| `grpc.port` | `--grpc-port` | gRPC 端口 |
| `ws.port` | `--ws-port` | WS 端口 |
| `log.level` / `log.format` | 固定 `info` / `json` | 日志 |

**绝不写入**：`enroll_token`（一次性凭据）、`node_secret`（注册后换得，存 `node-identity.json`）。

## 4. 验收标准

> **真机过**（最终由用户在波 3 整条走查验证）。Agent 保证逻辑 + 单测 + `go build/vet/test ./...` 绿。

- [ ] **AC1 未配置自检进 setup**：模拟全新机器（删 `worker.yml`/`worker.yaml` + 无 `<data-dir>/etc/node-identity.json`）→ 跑 `worker` → 进入 setup（TTY 交互 / 无 TTY 读参数）。
- [ ] **AC2 交互式采集**：有 TTY 时逐项提示 CP/token/name（+ 可选端口/data_dir），给默认值，回车接受默认，token 空则重问。
- [ ] **AC3 无人值守**：无 TTY 时从 `--control-plane`/`--token`/`--name`/`--grpc-port`/`--ws-port`/`--data-dir` + `JIANMANAGER_*` env 读；缺 CP 地址或 token → 明确报错退出（不卡住等输入）。
- [ ] **AC4 写 worker.yml**：setup 写出 `worker.yml`，字段（name/control_plane/grpc.port/ws.port/log）正确；**enrollment token 不在文件中**；data_dir 仅显式给出时写。
- [ ] **AC5 注册 + 持久化身份**：setup 携 token 经 gRPC 首注册成功，换得 `node_uuid`/`node_secret` 写 `<data-dir>/etc/node-identity.json`（0600）。
- [ ] **AC6 转 run + 上线**：setup 后转入正常 run，节点在面板显示在线（不重复消费 token、不二次注册）。
- [ ] **AC7 已配置直接 run**：有 `worker.yml` 或有 `node-identity.json` 的节点 → 跳过 setup，直接 run（现状不变，零行为变化）。
- [ ] **AC8 守不变量**：注册走 gRPC（不改 proto）；token 不落 `worker.yml`；Worker 不直连 DB。
- [ ] **AC9 质量门**：`go build ./...`、`go vet ./...`、`go test ./...` 全绿；新增逻辑有单测（未配置判定、无 TTY 参数/env 解析、worker.yml 写出字段；注册可 mock，TTY 交互路径难自动化但至少非 TTY 路径有测试）。

## 5. 真机验收步骤（建议，供波 3 走查）

1. 准备一个干净目录（无 `worker.yml`/`worker.yaml`，`./data/etc/node-identity.json` 不存在），放入 Worker 二进制。
2. 面板「添加节点」生成 enrollment token。
3. **交互式**：终端直接跑 `./worker`，逐项填 CP 地址 / token / 节点名 → 观察写出 `worker.yml`（确认无 token）、`./data/etc/node-identity.json` 生成（0600）、节点面板在线。
4. **无人值守**：另一干净目录跑 `./worker --control-plane <cp:9100> --token <jmet_...> --name edge-1`（或经 `JIANMANAGER_ENROLL_TOKEN` env），无交互直接 setup + 注册 + run，节点面板在线。
5. **缺项报错**：无 TTY 且不给 token（`./worker --control-plane <cp> < /dev/null`）→ 明确报错退出、不卡住。
6. **已配置直起**：对步骤 3 的目录再次跑 `./worker` → 不再 setup，直接 run（沿用既有身份重注册），节点在线。

## 6. 不做（范围外）

- 安装脚本退化（「cwd 有完整 worker 跳下载 / 下载与上线分两步 / 脚本调 setup」）→ **FR-223**。
- 终端断连 UX（FIX-B）、启停 kill 竞态（FIX-C）、首次上线真机断点（FIX-D）→ 各自 fix。
- `.yml` 约定本身（`.yaml`→`.yml` + viper 搜索）→ **FR-224**（已落地，本 FR 依赖其成果）。
