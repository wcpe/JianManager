# ADR-020: 节点 enrollment 一键安装与部署机制

- **日期**: 2026-06-23
- **状态**: accepted
- **上下文**: FR-004 已实现 Worker Node 启动时经 gRPC `Register` 注册到 Control Plane、换取 `node_uuid`/`node_secret` 并以 `node_secret` 鉴权心跳。但当前注册是「**无凭据自助注册**」：任何能连到 CP gRPC 端口的进程，只要给个 `name` 就能注册成节点（`Register` 不校验任何调用方身份），且 Worker 的运行参数全靠手工设置环境变量（`cmd/worker/main.go` 只读 env，配套的 `internal/worker/config.go` / `configs/worker.yaml` 形同虚设、从未被加载）。运营者要新增一台节点，得手动：拷二进制、设一堆 `JIANMANAGER_*` 环境变量、保证 CP 地址可达、手动常驻进程。FR-080 要求「**傻瓜部署**」：面板「添加节点」生成一条一键命令，到目标机器粘贴执行即自动装好、注册、上线。这要求解决两件事——(1) 注册必须**带凭据**（不能让陌生进程白嫖注册），(2) 安装/配置/常驻必须**脚本化**。同时面板自更新（FR-081，CP/Worker 二进制在线升级）与本主题同属「节点分发与运维」范畴，其分发来源/校验/编排的取舍一并在此立。

## 决策

引入 **enrollment token（一次性、限时的节点准入凭据）** 作为新增节点的信任锚，配套**平台分发的安装脚本**完成下载/写配置/注册/可选常驻，把 FR-004 的「自助注册」收敛为「**凭 enrollment token 注册**」。

### 1. enrollment token：一次性 + 限时的准入凭据

- **签发**：CP 新增 `POST /nodes/enroll-token`（仅平台管理员）。生成 32 字节随机明文（前缀 `jmet_`，base64url），**落库只存其 SHA-256 哈希**（同构 FR-086 拉取密钥、JM 既有运行时密钥惯例），明文仅签发响应一次性返回、不可二次读取。token 记录带：可选预设节点名（`node_name`，留空则注册时由 Worker 上报名生效）、过期时间（默认 30 分钟，可配 `ttl_minutes`）、消费状态（`used` + `used_at` + `used_by_node`）。
- **校验**：Worker 注册时携带 enrollment token；CP 在 `Register` 内校验——存在 + 未过期 + 未消费，三者全过才放行注册，校验通过后**立即原子标记 `used`**（一次性：同一 token 第二次注册被拒）。校验失败回 gRPC `PermissionDenied`，Worker 据此明确报错退出（非重试，避免无效 token 无限重试刷日志）。
- **token 经 gRPC metadata 传递、不改 proto**：复用 FR-004 心跳 `node_secret` 经 metadata 传递的既有手法（header `enroll-token`），不动 `RegisterRequest` 结构。**向后兼容**：未带 token 的 `Register`（FR-004 阶段的旧 Worker、或既有已注册节点重启后的重注册）按既有「自助注册/重注册」路径放行——enrollment token **只对「新节点首次落库」这一步设门槛**，已存在节点（按 name 命中）的重注册不强制 token（否则既有节点重启全部失败）。安全收口的彻底化（强制所有注册带凭据）留作后续 FR，避免破坏在网节点。

> **为何选「token 经 metadata + 仅卡新节点」而非「改 proto 加必填字段 + 卡所有注册」**：后者会让所有 FR-004 阶段已部署、已在网的节点在下次重启时注册失败、集体掉线，是破坏性变更。前者最小侵入、与既有 `node_secret` 鉴权同构、且把「准入」（新节点，token）与「续存」（老节点，node_secret）两件事分开，各自用恰当的凭据。

### 2. 安装脚本：平台分发的一键装机

- 形态：`scripts/install-worker.sh`（Linux/macOS，POSIX sh）+ `scripts/install-worker.ps1`（Windows PowerShell）。脚本由仓库维护、随发布分发（也可由 CP 静态托管，本 FR 先落仓库脚本 + 面板生成调用命令）。
- 职责（幂等）：
  1. 解析参数：CP 地址（gRPC + HTTP）、enrollment token、节点名（可选）、二进制下载地址（可选，缺省走 CP 约定的下载端点/release）、安装目录、是否装系统服务。
  2. 探测平台（os/arch）→ 下载对应 Worker 二进制到安装目录（下载源未就绪时**支持 `--binary` 指向本地已拷贝的二进制**，便于内网/离线与本 FR 真机自测）。
  3. 写 `worker.yaml`（含 CP gRPC 地址、grpc/ws 端口、data_dir、日志）——配置落盘而非堆环境变量。enrollment token **不写入 `worker.yaml`**（一次性凭据不留盘），改经环境变量/命令行传给首次启动的 Worker。
  4. 启动 Worker 完成首次注册（携带 enrollment token），换取 `node_uuid`/`node_secret` 并由 Worker **持久化到本地状态文件**（见 §3）。
  5. 可选注册系统服务：Linux 写 `systemd` unit（`jianmanager-worker.service`，`Restart=always`）、Windows 经 `sc.exe`/`New-Service` 注册服务，使节点开机自启、常驻自连。
- 一键命令形态（面板生成、可直接粘贴）：
  - Linux：`curl -fsSL <cp>/install-worker.sh | sh -s -- --control-plane <cp-grpc> --token <jmet_...> [--name ...] [--service]`
  - Windows：`iwr <cp>/install-worker.ps1 -UseBasicParsing | iex; Install-JianManagerWorker -ControlPlane <cp-grpc> -Token <jmet_...> [-Name ...] [-Service]`（PowerShell 等价）
- **实现落地**：签发端点 `POST /api/v1/nodes/enroll-token`（+ `GET /nodes/enroll-tokens`、`DELETE /nodes/enroll-tokens/:id`，均限平台管理员）。一键命令里的 CP gRPC 地址与脚本下载基址默认由签发请求 Host 推断，可经 CP 配置 `enroll.advertise_grpc` / `enroll.script_base_url` 显式覆盖；`enroll.binary_url` 非空则并入命令的 `--download-url`（缺省走脚本 `--binary` 本地兜底）。本地身份文件实现为 `internal/worker/register/identity.go`（原子写 + 0600）。

### 3. Worker 侧 enrollment 与凭据持久化

- Worker 启动时按优先级取 enrollment 入参：命令行 `--enroll-token` / 环境变量 `JIANMANAGER_ENROLL_TOKEN`。
- **凭据持久化**：注册成功换得的 `node_uuid`/`node_secret` 写入数据根下的本地状态文件 `etc/node-identity.json`（沿用 ADR-010 数据根 FHS 布局，`etc/` 存本地配置/身份）。Worker 重启时**优先读该文件**复用既有身份，**不重复消费 enrollment token**（token 已一次性失效；老节点重注册走 name 命中路径，§1）。状态文件含 `node_uuid`/`node_secret`，权限 0600，绝不回传日志。
- 与 FR-004 的关系：Worker 注册逻辑（`internal/worker/register`）扩展为「带 token 注册」；心跳鉴权（`node_secret`）完全不变。首次 vs 复启的分支：有本地身份文件 → 直接用其 `node_uuid`/`node_secret` 注册（重注册，不带 token）；无 → 必须带 enrollment token 首注册。

### 4. 面板自更新（FR-081）的来源/校验/编排立场（本 ADR 立、FR-081 落）

- **分发来源**：可配更新源（release feed / 私有 URL），与安装脚本的二进制下载源同源同治。
- **完整性校验**：下载产物必须带 **SHA-256** 校验（同构制品库 ADR-011、客户端分发 ADR-022 的内容校验思路），校验不符拒绝替换。
- **编排**：CP 自更新（下载→校验→替换→平滑重启）；Worker 升级**经 CP gRPC 编排**（CP 通知/推送 → Worker 下载校验替换重启），daemon 启动方式下**不杀游戏服**（ADR-003 wrapper 隔离保证子进程存活）。逐节点进度 + 失败回滚/重试。仅平台管理员 + 审计（FR-015）。本 ADR 仅定原则，具体 RPC/端点由 FR-081 实现并按需补 proto。

## 理由

- **最小侵入、不破网**：token 经 metadata + 只卡新节点落库，复用既有 `node_secret`-over-metadata 手法，既给新增节点上了准入锁，又不让任何在网节点在重启时掉线。
- **凭据不留盘、一次性**：enrollment token 落库只存哈希、明文一次性返回、用后即焚，且不写进 `worker.yaml`；长期身份（node_secret）持久化到 0600 状态文件。准入凭据短命、续存凭据持久，各司其职。
- **配置落盘取代环境变量堆砌**：`worker.yaml` 终于被真正加载（顺带补上 FR-004 遗留的「配置文件形同虚设」缺口），运营者改配置改文件而非记一串 `JIANMANAGER_*`。
- **脚本而非内置安装器**：装机是宿主级操作（下载、写服务单元、改开机自启），用平台分发的 shell/ps1 脚本最直接、可审计、可离线改造；无需让 Worker 二进制自带一套跨平台服务管理逻辑。
- **自更新原则同源**：把 FR-081 的来源/校验/编排立场并入本 ADR，使「分发新节点」与「升级在网节点」共享同一套来源与 SHA-256 校验心智，避免两套割裂的分发信任模型。

## 后果

- 新增表 `node_enroll_tokens`（哈希、过期、消费状态、预设名）；AutoMigrate 自动建表。
- `Register` RPC 行为分叉：新节点（name 未命中）**必须**带有效 enrollment token；老节点（name 命中）重注册不强制 token。该分叉需在 handler 内明确实现并测试覆盖（有效/过期/已消费/缺失/老节点重注册五条路径）。
- Worker 新增本地身份文件 `etc/node-identity.json`（数据根内），迁移节点数据根即迁移身份；删除该文件 + 节点在 CP 仍存在 → 下次启动会因无 token 且 name 命中而走重注册（仍可上线），无 token 且 name 未命中（如改了名）则首注册失败。
- 安装脚本要求目标机器有基础工具（`curl`/`iwr`、`sh`/`pwsh`）；二进制下载源在本 FR 未架设公网 release 时，脚本以 `--binary` 本地路径兜底（真机自测路径），公网分发端点留作后续。
- 「自助注册可被陌生进程白嫖」的风险**对新节点已封堵**，但对「伪造已存在节点名重注册」仍开放（需 node_secret 才能心跳，但重注册本身不验 secret）——这是为不破网做的有意权衡，彻底收口留后续 FR。

## 替代方案

- **改 proto 给 `RegisterRequest` 加必填 enroll_token 字段、强制所有注册带 token**：会让所有在网的 FR-004 节点重启即掉线，破坏性过强；放弃（改为 metadata 传递 + 只卡新节点）。
- **mTLS / 双向证书做节点准入**：安全性最高，但要求 CP 签发并分发客户端证书、Worker 管理证书生命周期，部署复杂度对中小运营者过重，且与「一条命令粘贴即装好」的傻瓜部署目标相悖；放弃（enrollment token 是轻量准入，证书化留作未来增强）。
- **Worker 二进制内置跨平台服务安装器**（自带 systemd/Windows service 注册逻辑）：把宿主级运维耦合进业务二进制，跨平台分支多、难审计、难离线改造；放弃（用平台分发的脚本，宿主操作归脚本）。
- **enrollment token 写入 `worker.yaml` 长期保存**：一次性凭据留盘违背「短命准入」初衷、且重启会重复尝试消费已失效 token；放弃（token 只经 env/命令行传一次，长期身份另存 0600 状态文件）。
- **CP 主动 SSH 到目标机器装 Worker（推模式）**：要求 CP 持有目标机器凭据、打通 SSH 可达性，反向耦合且攻击面大；放弃（用「目标机器粘贴命令」的拉模式）。

## 关系

- **ADR-002（gRPC 节点通信）**：enrollment token 经 gRPC `Register`（metadata）校验，注册通路仍是唯一的 gRPC，未新增 RPC 协议。
- **ADR-003（守护进程 Wrapper）**：FR-081 Worker 升级在 daemon 模式下不杀游戏服，依赖 ADR-003 的 wrapper 子进程隔离；安装脚本注册的系统服务托管的是 Worker 主进程，与 wrapper 托管的游戏服进程正交。
- **ADR-010（便携数据根）**：Worker 身份文件 `etc/node-identity.json`、`worker.yaml` 默认数据根均沿用 FHS 布局，迁移数据根即迁移节点身份与配置。
- **ADR-011（制品库）/ ADR-022（客户端分发）**：二进制下载与自更新的 SHA-256 完整性校验思路与之同源（内容寻址 + 校验和），但节点分发面向运营者宿主、与面向玩家的客户端分发（拉取密钥 + 签名 manifest）是物理隔离的两套分发面。
- **FR-004（节点注册与心跳）**：本 ADR 扩展其 `Register` 为「凭 token 准入」，心跳 `node_secret` 鉴权不变。
- **FR-081（面板自更新）**：其分发来源/SHA-256 校验/CP-gRPC 编排/审计原则由本 ADR §4 确立，实现与按需补 proto 由 FR-081 完成。
