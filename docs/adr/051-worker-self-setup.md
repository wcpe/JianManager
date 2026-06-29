# ADR-051: Worker 免配置自启 setup（下载/上线分离）

- **日期**: 2026-06-29
- **状态**: accepted
- **取代关系**: **supersedes ADR-020 §2 的安装编排立场**（「平台分发单脚本一把梭：下载二进制 + 写 `worker.yml` + 以 token 注册 + 可选常驻」中「**脚本写配置**」那一段）。ADR-020 **仍 accepted**——其 enrollment token 准入模型（§1）、Worker 侧 enrollment 与凭据持久化（§3）、自更新来源/校验/编排（§4）立场全部不变、继续有效；本 ADR 只改写「谁来写配置、上线如何编排」：从「脚本写配置 + 脚本拉起 Worker 注册」改为「**下载二进制（外部步骤）+ Worker 自己 setup（写配置 + 注册 + run）**」。
- **关联**: ADR-002（gRPC 唯一 RPC 通路）、ADR-010（数据根 FHS / `etc/node-identity.json`）、ADR-039（UUID 锚定注册，修复重名覆写）、FR-222（本 ADR 落地）、FR-223（安装脚本据此退化为「取二进制 + 调 setup」）、FR-224（`.yml` 约定）。

## 上下文

ADR-020 / FR-080 把「新增节点」做成「面板生成一条一键命令，到目标机器粘贴执行」：平台分发的 `install-worker.sh` / `.ps1` 一把梭完成「探测平台 → 下载二进制 → **写 `worker.yml`** → 以 enrollment token 拉起 Worker 首注册 → 可选注册系统服务」。这条链路能用，但把「**配置怎么写**」这件本属 Worker 自身职责的事，外包给了一段 shell/PowerShell：

- **新机器强依赖脚本**：没有那段脚本（或脚本在某平台/某 shell 跑不通、被企业策略拦了 `curl | sh`），就没法写 `worker.yml`、没法上线——即便二进制已经在手。
- **配置字段散落两处**：`worker.yml` 的字段由脚本里的 `echo` 拼（`install-worker.sh` §3），与 Worker 配置 struct（`internal/worker/config.go`）两边维护，易漂移。
- **「下载」与「上线」耦合在一条命令里**：参考 GitHub Actions Runner 的体验——「下载 Runner」与「配置并上线 Runner（`./config.sh` / `./run.sh`）」是分开的两步，下载归下载、上线归 Runner 自己。JM 的一键脚本把两者糅在一起，新机器上线必须走脚本这条独木桥。

用户拍板：**worker 启动入口自检——若未配置就启动 setup**（不是分离的 `configure`/`run` 子命令，而是 `run` 入口前置一道自检）。「下载」（取二进制）与「上线」（setup + 注册 + run）解耦：Worker 自己负责上线，下载是外部步骤。

## 决策

### 1. Worker 入口前置「未配置自检」，未配置即进 setup

`runWorker` 在加载配置前先判定是否已配置：

```
未配置 ⇔ (无 worker.yml 配置文件，含 .yaml 回退) 且 (无 <data-dir>/etc/node-identity.json)
```

- **两者皆缺** → 全新机器，进入 setup。
- **任一存在** → 已配置（有 yml = 写过配置；有 node-identity = 注册过身份）→ 跳过 setup，走现有 run（**现状零变化**）。
- 命令行显式传配置文件路径（`worker /path/worker.yml`）也算已配置（用户显式给了配置）。
- 身份文件路径 `<data-dir>/etc/node-identity.json`，`<data-dir>` 按 `dataroot.Resolve` 同优先级解析（`--data-dir` > env > `./data`），**只解析路径不建目录**（自检无副作用）。

判定刻意取「**任一存在即已配置**」而非「两者皆备」：身份在、yml 没了，是「配置被删但已注册过」的运维场景，应走现有重注册路径（据身份文件重注册），不该重新 setup 覆盖既有身份。

### 2. setup 双形态：交互式（TTY）与无人值守（无 TTY）

setup 探测 stdin 是否 TTY，分两路采集入参：

- **有 TTY（交互式）**：逐项提示 CP gRPC 地址、enrollment token、节点名（可空），可选 grpc/ws 端口与 data_dir，均给默认值、回车接受默认；token 必填（空则重问）；端口非法重问；EOF 明确报错退出（不死循环）。
- **无 TTY（CI / 管道 / systemd / Windows 服务）**：**不阻塞等输入**，从命令行参数 + 环境变量读（`--control-plane`/`--token`/`--name`/`--grpc-port`/`--ws-port`/`--data-dir` + `JIANMANAGER_*`、`JIANMANAGER_ENROLL_TOKEN`），优先级 flag > env > 默认；缺必填项（CP 地址 / token）→ `stderr` 打印可操作错误 + 非零退出（非交互环境卡住等输入是死锁）。

一套 setup 覆盖「人在终端敲」与「脚本/服务无人值守」两种上线场景，无需两个子命令。

### 3. setup 产物与编排（写 yml → 注册 → 持久化身份 → 转 run）

采集到入参后顺序执行，任一步失败即明确报错退出（不留半截状态）：

1. **写 `worker.yml`**（原子写，复刻 `install-worker.sh` 同一组字段：name / control_plane / data_dir(显式时) / grpc.port / ws.port / log）。**enrollment token 绝不写入**（一次性凭据不留盘，沿用 ADR-020 §2.3 / §3）。
2. **携 token 首注册**（复用 `internal/worker/register`，走 gRPC，token 经 metadata header `enroll-token`，**不改 proto**；沿用现有指数退避重试）。
3. **持久化身份**：换得的 `node_uuid`/`node_secret` 写 `<data-dir>/etc/node-identity.json`（`register.SaveIdentity`，0600 原子写，含敏感 secret 不入日志，沿用 ADR-020 §3 / ADR-010）。
4. **转入正常 run**：setup 不退出进程，把内存中构造的配置 + 首注册身份交给 run 主体；run 主体识别「身份已由 setup 持久化」则跳过重复注册，直接以该身份起服务与心跳。

## 理由

- **新机器零脚本依赖、丝滑**：只要有 Worker 二进制本身就能上线——直接 `./worker` 交互填三项，或 `./worker --control-plane ... --token ...` 无人值守。不再被「那段 shell 能不能跑通」卡住。这是相对 ADR-020 单脚本模型最大的体验提升。
- **配置归属正位**：「怎么写 `worker.yml`」回到 Worker 自身（配置 struct 与写出逻辑同源），消除「脚本 `echo` 拼字段 vs 配置 struct」两处维护的漂移源。
- **下载 / 上线解耦（对齐 GitHub Actions Runner 心智）**：下载归外部（脚本 / 手动），上线归 Worker。脚本（FR-223）退化为「取二进制 + 调 setup」，职责更单一、更可审计。
- **TTY / 无人值守双形态**：人工开节点（交互）与批量/服务化部署（无人值守）都顺，且无 TTY 缺项 fail-fast、绝不卡死。
- **不破网、不破约束**：注册仍唯一走 gRPC（ADR-002）、token 仍不落盘（ADR-020）、身份仍 0600（ADR-010）、Worker 仍不直连 DB；已配置节点行为零变化。

## 后果

- Worker 入口新增「未配置自检 + setup」分支；新增 `internal/worker/setup` 包（采集 + 写 yml + 编排）。配置包 `FindConfigFile` 导出供自检复用。
- `install-worker.sh` / `.ps1` 与 setup **并存**：脚本仍可预写 yml 后启 Worker（自检判已配置直接 run），不破坏既有一键安装；脚本退化为「取二进制 + 调 setup」由 FR-223 落地（本 ADR 不动脚本）。
- 「上线必须走平台分发脚本」不再成立——但脚本仍是「下载 + 常驻服务注册」的便利封装（systemd / Windows 服务那部分仍归脚本，Worker 二进制不内置跨平台服务安装器，沿用 ADR-020 替代方案的取舍）。
- 交互式 setup 路径难自动化测试（需伪 TTY），以「无 TTY 参数/env 路径 + 未配置判定 + yml 写出 + 身份持久化」单测覆盖；交互路径靠真机走查（FR-222 验收）。

## 替代方案

- **维持 ADR-020 单脚本一把梭（脚本写配置）**：新机器强依赖脚本、配置字段两处维护、下载/上线耦合；本 ADR 改写之（保留脚本作下载/常驻便利封装）。
- **分离 `configure` / `run` 两个子命令（仿 `./config.sh` + `./run.sh`）**：要求用户记两条命令、先 configure 再 run，多一步心智；用户拍板用「run 入口自检，未配置即 setup」一条命令更顺（已配置直接 run、未配置自动 setup，无需用户区分阶段）。放弃。
- **Worker 二进制内置跨平台系统服务安装器**：把宿主级运维（systemd / Windows service 注册、开机自启）耦合进业务二进制，跨平台分支多、难离线改造；沿用 ADR-020 取舍，常驻服务仍归脚本，Worker 只管「写配置 + 注册 + run」。放弃。
- **setup 写完 yml 后 `exec` 重启自身读 yml 再 run**：多一次进程重启 + 重读文件 IO + 重新注册（或重读身份）的竞态；改为 setup 在内存直接构造配置 + 复用首注册身份转 run，无重启。放弃。
