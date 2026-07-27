# 功能规格：SSH 推送式远程部署脚本

> 状态：审核通过·开发中　·　关联 PRD：FR-277　·　分支：dev（脚本+文档，小体量不另起 feature 分支）　·　关联 ADR：ADR-063（新增）、ADR-051 / ADR-020（复用其安装编排语义）

## 1. 背景与目标

现有部署路径都是**拉取式**：目标机上跑 `install-worker.{sh,ps1}`（`curl | sh`，FR-080/222/223），且**面板（Control Plane）完全没有部署脚本**——部署 CP 只能手动 scp + 手写 systemd unit。运营者在本地开发机（Windows git-bash / Linux / macOS）持有 SSH 密钥与 `make dist` 产物，需要一条**推送式**通道：从操作机经 SSH 把面板或节点部署 / 更新到远程 Linux 主机，全程环境变量配置、可重复执行。

目标（P1）：新增 `scripts/deploy-cp.sh` 与 `scripts/deploy-worker.sh` 两个独立 POSIX sh 脚本，操作机执行，经 SSH 密钥认证推送本地 `dist/` 产物完成首次部署与更新部署。

## 2. 需求（要什么）

- 两个独立脚本，共享同一套 `JM_*` 环境变量约定与公共函数（内嵌，不引第三方依赖，仅要求操作机有 `ssh`/`scp`/`sh`）。
- SSH 密钥认证（不支持密码交互）；主机 IP / SSH 端口 / 用户 / 密钥路径 / 安装目录 / 服务端口等全部经环境变量配置。
- 首次部署 = 推二进制 + 建目录 + 配置 + systemd unit + 启动 + 健康检查；更新部署 = 停服务 → 换二进制 → 重启，数据 / 配置 / 节点身份全保留；同一脚本幂等重复执行，自动识别首次 / 更新。
- **三档目标机权限全支持**（用户拍板 2026-07-11）：
  1. **root 直连**：系统级 systemd（`/etc/systemd/system/`），现状语义。
  2. **非 root + 免密 sudo**：同系统级 systemd，特权步骤经 `sudo -n` 提权。
  3. **纯普通用户（无 sudo）**：systemd **user unit**（`~/.config/systemd/user/` + `systemctl --user`），安装目录退到 `~/`，依赖 linger 保证 SSH 断开后服务常驻。
- Worker 首次上线经 `JM_ENROLL_TOKEN`（面板「添加节点」签发的一次性 `jmet_` 令牌）完成注册，token 不落盘（沿 ADR-020 §2.3）。
- 本地 `dist/` 缺 linux 产物时报错并提示 `make dist`（不静默触发长构建；`JM_BUILD=1` 时自动触发）。
- **范围内**：Linux amd64/arm64 目标主机 + systemd（system 与 user 两级）。
- **不做（范围外）**：Windows 目标主机（pull 式 `.ps1` 已覆盖）、`deploy-all` 编排、MySQL 等外部依赖安装、更新失败自动回滚（保留上一版二进制 `.bak` 供手动回退即可）、多主机批量部署、密码认证、带密码提示的 sudo（只认免密 `sudo -n`）。

## 3. 设计（怎么做）

架构决策见 **ADR-063**（push 式部署脚本定位：Worker 侧完全复用 ADR-051「取二进制 + 调 worker setup」两阶段语义与既有 `install-worker.sh`，不另起第二套上线逻辑；CP 侧新增最小 systemd 承载；服务档位 system/user 双轨）。要点：

### 3.1 环境变量契约（新外部接口）

前缀 `JM_`（操作机侧脚本自用），与 `JIANMANAGER_`（目标机上二进制消费）隔离，避免误透传。

| 变量 | 适用 | 默认 | 说明 |
|---|---|---|---|
| `JM_SSH_HOST` | 共同 | **必填** | 目标主机 IP / 域名 |
| `JM_SSH_PORT` | 共同 | `22` | SSH 端口 |
| `JM_SSH_USER` | 共同 | `root` | SSH 用户 |
| `JM_SSH_KEY` | 共同 | 空 | 私钥路径；空 = 走 ssh 默认密钥链 / agent |
| `JM_SERVICE_SCOPE` | 共同 | `auto` | `system` / `user` / `auto`。auto：root 或免密 sudo → system，否则 → user |
| `JM_DIST_DIR` | 共同 | `./dist` | 本地产物目录 |
| `JM_BUILD` | 共同 | `0` | `1` = 产物缺失时自动 `make dist` |
| `JM_INSTALL_DIR` | 共同 | system 档：CP `/opt/jianmanager-cp`、Worker `/opt/jianmanager`；user 档：`~/jianmanager-cp`、`~/jianmanager` | 目标机安装目录 |
| `JM_DATA_DIR` | 共同 | `<install-dir>/data` | 目标机数据根 |
| `JM_CP_HTTP_PORT` | CP | `8080` | 面板 HTTP 端口 |
| `JM_CP_GRPC_PORT` | CP | `9100` | 面板 gRPC 端口 |
| `JM_CONTROL_PLANE` | Worker | 首次必填 | CP gRPC 地址 `host:port` |
| `JM_ENROLL_TOKEN` | Worker | 首次必填 | 一次性 enrollment token（更新部署不需要） |
| `JM_NODE_NAME` | Worker | 空 | 节点名（空 = CP 预设名） |
| `JM_WORKER_GRPC_PORT` | Worker | `9101` | Worker gRPC 端口 |
| `JM_WORKER_WS_PORT` | Worker | `9102` | Worker WS 终端端口 |

架构探测：脚本先 `ssh uname -m` 探测目标机架构选 `dist/*-linux-{amd64,arm64}`（arm64 产物缺失时明确报错，提示扩 `make dist`；v1 `dist-bin` 仅出 amd64，arm64 属产物侧现状约束，不在本 FR 扩）。

### 3.2 服务档位（scope）判定与执行差异

远端一次探测收集：`id -u`（是否 root）、`sudo -n true` 是否可用、`uname -m`。

| | system 档（root 或 sudo） | user 档（纯普通用户） |
|---|---|---|
| unit 路径 | `/etc/systemd/system/<svc>.service` | `~/.config/systemd/user/<svc>.service` |
| systemctl | `systemctl ...`（非 root 前缀 `sudo -n`） | `systemctl --user ...`（远端调用前置 `XDG_RUNTIME_DIR=/run/user/$(id -u)`，SSH 非交互会话下 user bus 才可达） |
| 常驻保证 | systemd 系统级天然常驻 | **linger**：预检 `loginctl show-user $USER -p Linger`；未开则尝试 `loginctl enable-linger $USER`（部分发行版允许自开），失败则**硬报错**并指引「让管理员执行一次 `loginctl enable-linger <user>`」——不开 linger 的 user 服务在 SSH 断开后会被杀，静默降级不可接受 |
| 特权步骤提权 | 非 root 时 `sudo -n` 前缀（写 unit / systemctl / 建 /opt） | 无需提权（全在 $HOME） |

`JM_SERVICE_SCOPE=auto` 判定：root → system；非 root 且 `sudo -n true` 成功 → system；否则 → user。显式 `system` 但既非 root 又无免密 sudo → 预检报错。

### 3.3 deploy-worker.sh（复用既有资产，最薄）

1. 预检：本地产物在（否则按 `JM_BUILD` 决定构建或报错）；`ssh -o BatchMode=yes` 连通（失败给密钥指引）；scope 判定（§3.2，user 档含 linger 检查）。
2. `scp dist/worker-linux-<arch>` → `<install-dir>/jianmanager-worker.new` + `scp scripts/install-worker.sh` → 目标机临时路径。
3. 远端判定**首次 / 更新**：`systemctl [--user] cat jianmanager-worker` 存在 = 更新，否则首次。
   - **首次**：原子就位二进制后远程执行 `install-worker.sh --binary <bin> --service [--service-scope user] --control-plane $JM_CONTROL_PLANE --token $JM_ENROLL_TOKEN [--name ...] --ws-port ... --install-dir ... --data-dir ...`（system 档非 root 时整脚本 `sudo -n` 执行）——unit 写出、worker 自配 setup、注册、token 不落普通文件，全部走 FR-223/ADR-051 既有已验路径。**为 user 档扩 `install-worker.sh` 本身**（新增 `--service-scope system|user`，默认 system 保持现状零变化）：unit 写 `~/.config/systemd/user/`、`systemctl --user`、[Install] 段 `WantedBy=default.target`；扩完同步内嵌副本（`make embed-install-scripts`，字节一致测试守护）。pull 式一键安装同获非 root 能力，属本 FR 的阻塞性依赖而非顺手加功能。
   - **更新**：`systemctl [--user] stop` → 旧二进制 `mv` 为 `.bak` → 新二进制就位 → `start`。不碰 `worker.yml` / `etc/node-identity.json` / unit（身份与配置保留，重启后以既有身份重连，无需 token）。
4. 验证：`systemctl [--user] is-active jianmanager-worker` = active，失败时抓 `journalctl [--user] -u jianmanager-worker -n 40` 回显。

### 3.4 deploy-cp.sh（净新，但同构）

1. 预检同上（scope 判定含 linger）。
2. `scp dist/control-plane-linux-<arch>` → `<install-dir>/jianmanager-cp.new`。
3. 远端判定首次 / 更新（`systemctl [--user] cat jianmanager-cp`）：
   - **首次**：建 `<install-dir>`/`<data-dir>`；生成**最小** `control-plane.yml`（`server.host=0.0.0.0`、`server.port`、`grpc.port`、`dev_mode: false`、sqlite dsn 指向数据根；其余全部吃程序默认值——样例 yml 的可选段不铺开）；按 scope 写 unit（`WorkingDirectory=<install-dir>`、`Environment=JIANMANAGER_DATA_DIR=<data-dir>`、`ExecStart=<bin> <install-dir>/control-plane.yml`、`Restart=always`；user 档 `WantedBy=default.target`）；enable + start。JWT secret / WS 密钥不写死：生产态 CP 自动生成持久化（ADR-061 三轨），零敏感信息进脚本。
   - **更新**：stop → `.bak` → 换二进制 → start。**不重写 `control-plane.yml` 与 unit**（已存在的配置视为运维现场，不覆盖）。
4. 验证：轮询 `curl -fsS http://127.0.0.1:<http-port>/`（远端本机探活，避免防火墙干扰判定）至 HTTP 可达，超时抓 journal 回显；成功后打印面板外部地址与「下一步：浏览器完成首启引导（FR-017）→ 添加节点签 token → JM_ENROLL_TOKEN=... 跑 deploy-worker.sh」。

### 3.5 公共约定

- 两脚本头部内嵌同一组小函数（`rsh`/`rcp` 包装 `ssh -p/-i`、`die`、scope 探测、首次/更新判定），**不抽共享 lib 文件**（两份 ~40 行重复换取单文件可独立分发，与 install-worker.sh 单文件立场一致）。
- 所有远端变更点幂等：`mkdir -p`、unit 重写仅首次、二进制替换原子（`.new` 就位后 `mv`）。
- `set -eu`；任何一步失败带上下文退出，不留半截（二进制 `.new` 残留无害）。
- `--dry-run`（或 `JM_DRY_RUN=1`）：打印将执行的远端步骤计划不实际连接，供自测断言与用户预检。

## 4. 任务拆分

- [x] ADR-063：push 式 SSH 部署脚本定位（Worker 复用 ADR-051 语义 / CP 最小 systemd 承载 / system+user 双档 / JM_* 契约隔离）
- [x] `install-worker.sh` 扩 `--service-scope system|user`（默认 system 现状零变化；user 档 unit 路径 / `systemctl --user` / `WantedBy=default.target`）+ 内嵌副本同步（字节一致测试绿）；另扩 token 经 `JIANMANAGER_ENROLL_TOKEN` env 缺省读取（deploy 远程调用时 token 不进命令行）
- [x] `scripts/deploy-worker.sh`（预检 / scope 判定 / 推送 / 首次走 install-worker.sh / 更新换二进制 / 验证 / dry-run）
- [x] `scripts/deploy-cp.sh`（预检 / scope 判定 / 推送 / 首次最小配置+unit / 更新换二进制 / 探活验证 / dry-run）
- [x] 测试：三脚本 `sh -n` 全绿 + dry-run/缺参/非法档位断言 + mock ssh 六场景（CP root 首装 / CP root 更新 / CP user 档首装 / Worker user 档首装 / Worker sudo 更新 / 档位冲突拒绝）捕获远端脚本 `sh -n` 与内容断言全过 + `go build ./...` 绿
- [x] 文档同步：README「SSH 推送部署」章节 + 环境变量表；ARCHITECTURE §12.2；CHANGELOG；PRD FR-277 → 🔨 开发中
- [x] 真机验收（见 §5.7；2026-07-12 Debian user 档全过，含更新幂等 + 探活端口修复）

## 5. 验收标准

1. `deploy-cp.sh`：仅设 `JM_SSH_HOST`（其余默认）对全新 Linux 主机执行，结束时远端 `jianmanager-cp` 服务 active、`curl http://<host>:8080` 可达面板；脚本回显下一步指引。
2. `deploy-worker.sh`：设 `JM_CONTROL_PLANE` + `JM_ENROLL_TOKEN` 首次执行后，`jianmanager-worker` active 且节点在面板显示在线；token 不出现在远端任何普通持久化文件（unit 含 token env 与现状 FR-223 一致，属既有已接受行为，不新增暴露面）。
3. **user 档**：纯普通用户主机（无 sudo）上两脚本以 user unit 部署成功，SSH 断开后服务仍存活（linger 生效）；linger 无法开启时报错并给管理员指引而非静默降级。
4. 更新部署：改动本地代码重出 dist 后重复执行两脚本，服务重启且版本更新（日志/`--version` 佐证）；CP 的 `control-plane.yml`、数据库、Worker 的 `worker.yml`、`node-identity.json` 均未被改写；Worker 更新未设 token 也成功。
5. 幂等：连续执行两次同一脚本无报错、结果一致。
6. 预检失败路径：SSH 不通 / 产物缺失 / 首次缺 token / 显式 system 档无提权 / user 档 linger 不可开，均给出可操作中文报错而非莫名堆栈。
7. **真机验收（必须，用户确认）**：真实 Linux 主机——先查操作机 `~/.ssh` 密钥（无则生成 ed25519 公钥交用户装入主机）→ 用户提供 IP/端口/用户 → `deploy-cp.sh` 部面板 → 浏览器首启引导 + 签 token → `deploy-worker.sh` 部节点 → 面板节点在线 → 两脚本各再跑一次更新部署复验 §5.4/§5.5。按用户主机实际权限档执行（用户已确认目标机为纯普通用户 → 真机主验 user 档；system 档以 dry-run 断言 + 可得 root 环境时补验）。单测 / 静态检查绿不替代本项。
   - **[x] 已通过（2026-07-12，Debian 目标机 103.45.143.199:1001，纯普通用户 `jianmanager`，user 档）**：`deploy-cp.sh` 部面板（HTTP 50100，避开仅放行 >50100 端口的安全组）→ 真浏览器登录 + 面板「添加节点」签 token → `deploy-worker.sh` 部节点（WS 50102）→ 面板 node-main **在线**（linux/amd64、4 核、实时指标）；CP+Worker 各再跑一次更新部署（**均不带首次参数/token**）→ 节点身份 md5 不变、`control-plane.yml`/`worker.yml` mtime 不变、DB 仅服务写时序增长、`.bak` 各留一份、更新后节点仍在线同一 UUID。真机复验发现并当场修复：**CP 更新部署探活端口误用默认 8080**（更新不重传端口时探活错端口误报），改为更新模式从远端 `control-plane.yml` 的 `server.port` 解析（awk 限定 server 段避开 grpc 同名 port），修复后探活正确读到 50100 通过。

## 6. 风险 / 待定

- **linger 需一次管理员授权**：多数发行版 `loginctl enable-linger` 自开需要 polkit 管理员授权；脚本尝试自开、失败即报错给指引。完全零管理员参与做不到「常驻」这一档，属 systemd 平台约束。
- **user 档低位端口**：user 档下 CP 若配 <1024 端口无法监听；默认 8080/9100/9101/9102 均高位，不受影响。
- **`systemctl --user` 的会话总线**：SSH 非交互会话需 `XDG_RUNTIME_DIR` 正确指向 `/run/user/<uid>`（linger 开启后该目录常驻）；脚本远端命令统一前置导出。
- **arm64 产物**：`make dist` 现只出 amd64；目标机 arm64 时脚本报错提示，产物扩编不在本 FR。
- **首启引导依赖人工**：CP 首次部署后必须浏览器完成 FR-017 引导才能签 token，deploy-worker 无法紧随全自动——已在脚本回显中指引，属产品既定形态非缺口。
