# ADR-063: SSH 推送式远程部署脚本（push 部署定位与服务双档）

- **日期**: 2026-07-11
- **状态**: accepted
- **取代关系**: 无取代。补充 ADR-020（enrollment 准入与一键安装）与 ADR-051（Worker 免配置自启 setup、脚本退化为「取二进制 + 调 setup」）：在既有**拉取式**安装（目标机上 `curl | sh`）之外新增**推送式**通道（操作机经 SSH 推产物），Worker 上线语义完全复用二者、不另起炉灶。
- **关联**: FR-277（本 ADR 落地）、FR-080/222/223（拉取式一键安装现状）、ADR-010（数据根 FHS）、ADR-036（产物命名契约 `<component>-<os>-<arch>`）、ADR-061（CP 生产态密钥自动生成——CP 部署脚本因此零敏感信息）。

## 上下文

部署 JianManager 到远程主机现有两条路，都有缺口：

- **Worker**：拉取式 `install-worker.{sh,ps1}`（FR-080）要求登录目标机粘贴命令执行，且二进制来源默认 GitHub Releases——本地开发版（未发版 / 未 push）无从拉取；操作机往目标机「推」本地 `make dist` 产物没有脚本化通道。
- **Control Plane**：**完全没有部署脚本**。部署面板 = 手动 scp + 手写 systemd unit + 手写 `control-plane.yml`，每台主机重复一遍且无更新编排。

运营者的实际形态：本地开发机（Windows git-bash / Linux / macOS）持有 SSH 私钥与 `dist/` 交叉编译产物，需要「一条环境变量配置的命令」完成远程 Linux 主机上面板 / 节点的首次部署与更新部署。另外用户拍板目标机权限**三档全支持**：root 直连、非 root + 免密 sudo、纯普通用户（无任何提权）。

## 决策

### 1. 推送式脚本是「传输 + 编排」层，不是第二套安装逻辑

新增 `scripts/deploy-cp.sh` / `scripts/deploy-worker.sh`（POSIX sh，操作机执行）。定位：**把本地产物送到目标机 + 编排既有安装语义**，自身不重新发明「怎么上线」：

- **Worker 首次上线**：脚本把 `dist/worker-linux-<arch>` 与仓内 `install-worker.sh` 一起 scp 到目标机，远程执行 `install-worker.sh --binary <已推送二进制> --service ...`——unit 写出、worker 自配 setup（写 yml + 注册 + 持久化身份 + run）、token 经 env 不落普通文件，全部走 ADR-051/FR-223 已真机验证的路径。**push 脚本里没有任何 worker 上线逻辑**，install-worker.sh 是唯一真源。
- **CP 首次部署**：无既有资产可复用，deploy-cp.sh 自持最小承载：推二进制 + 生成**最小** `control-plane.yml`（端口 + sqlite + `dev_mode: false`，其余吃程序默认值）+ systemd unit + HTTP 探活。JWT / WS 令牌密钥**不写**：CP 生产态自动生成持久化（ADR-061），脚本零敏感信息。
- **更新部署（两者同构）**：远端 `systemctl cat <svc>` 判「已装」→ stop → 旧二进制留 `.bak` → 新二进制原子就位 → start。**不碰**既有 `control-plane.yml` / `worker.yml` / `node-identity.json` / unit（运维现场不覆盖；Worker 以持久化身份重连，无需 token）。同一脚本幂等，自动分派首次 / 更新。

### 2. 服务档位双轨：system 与 user unit

远端一次探测（`id -u`、`sudo -n true`、`uname -m`）后按 `JM_SERVICE_SCOPE`（默认 `auto`）分派：

| | system 档 | user 档 |
|---|---|---|
| 适用 | root 直连；非 root + 免密 sudo（特权步骤 `sudo -n` 前缀） | 纯普通用户 |
| unit | `/etc/systemd/system/` | `~/.config/systemd/user/`，`WantedBy=default.target` |
| systemctl | `systemctl` | `systemctl --user`（前置 `XDG_RUNTIME_DIR=/run/user/<uid>`） |
| 常驻 | 天然 | **linger 强制**：未开则尝试 `loginctl enable-linger`，失败**硬报错**给管理员指引——不开 linger 的 user 服务 SSH 断开即被杀，静默降级不可接受 |
| 安装目录默认 | `/opt/jianmanager[-cp]` | `~/jianmanager[-cp]` |

为此 **`install-worker.sh` 本身扩 `--service-scope system|user`**（默认 system，现状零变化）：user 档改写 unit 路径与 systemctl 调用。这是 push 脚本 user 档的阻塞性依赖，同时让拉取式一键安装顺带获得非 root 能力；内嵌副本经 `make embed-install-scripts` 同步，字节一致测试守护。

### 3. 环境变量契约：`JM_*` 前缀，与 `JIANMANAGER_*` 隔离

操作机侧脚本配置一律 `JM_*`（`JM_SSH_HOST/PORT/USER/KEY`、`JM_SERVICE_SCOPE`、`JM_INSTALL_DIR`、`JM_CP_HTTP_PORT`、`JM_CONTROL_PLANE`、`JM_ENROLL_TOKEN` 等，见 spec §3.1）。目标机上二进制消费的 `JIANMANAGER_*` 是另一命名空间，脚本按需**显式**映射（如 token → `JIANMANAGER_ENROLL_TOKEN`），绝不整段透传——避免操作机环境污染目标机配置。

### 4. 二进制来源：本地 `dist/` 推送，不在线拉取

push 脚本只认本地 `make dist` 产物（缺失时报错提示，`JM_BUILD=1` 才自动构建）；不做「目标机在线下载」——那是拉取式脚本与 CP 自更新（ADR-036 / FR-278 内嵌分发）的职责。由此未发版 / 未 push 的开发版可直接部署，与用户开发节奏匹配。

## 理由

- **单一真源**：Worker 上线逻辑只活在 install-worker.sh + worker setup（ADR-051）里；push 脚本坏不了上线语义，改上线语义也不用改两处。
- **更新不碰现场**：更新部署仅换二进制，配置 / 身份 / 数据全保留——与「配置归属 Worker 自身」（ADR-051）、「数据根单一」（ADR-010）一致，也让幂等重跑安全。
- **三档权限全覆盖但不静默降级**：sudo 只认免密（`sudo -n`，交互 sudo 在无 TTY 管道里就是卡死）；user 档 linger 开不了就报错——「部署成功但断开就死」比失败更糟。
- **零敏感信息进脚本 / 配置**：CP 密钥走 ADR-061 自动生成；worker token 一次性经 env；脚本可安心入库分发。

## 后果

- 新增 `scripts/deploy-cp.sh`、`scripts/deploy-worker.sh`；`install-worker.sh`（含内嵌副本）扩 `--service-scope`。
- 操作机要求：`sh` + `ssh` + `scp`（git-bash / Linux / macOS 原生具备），无其他依赖。
- user 档遗留平台约束：linger 自开在多数发行版需一次管理员授权；<1024 端口不可用（默认端口均高位，无碍）。
- 自动化测试以 `sh -n` + `--dry-run` 计划输出断言覆盖分支；端到端可信度靠 FR-277 真机验收（spec §5.7）。

## 替代方案

- **扩 install-worker.sh 加「远程模式」**（脚本自己 ssh 出去）：把传输层塞进安装脚本，职责混杂且 CP 侧无对应物；分层（deploy-* 管传输编排 / install-* 管本机安装）更清晰。放弃。
- **CP 也做一个 install-cp.sh 再由 deploy-cp.sh 调**：CP 无 worker setup 那样的自配上线逻辑可托付，多一层文件只是形式对称；CP 承载逻辑直接内嵌 deploy-cp.sh（首次仅 unit + 最小 yml）。放弃。
- **Ansible / rsync 等成熟工具**：给「两台主机、一把密钥」的目标用户引入工具链与学习成本，违背单文件脚本随仓分发的既有立场（install-worker.sh 同款取舍）。放弃。
- **更新失败自动回滚**：`.bak` 已留手动回退路径；自动回滚要可靠需健康判定 + 状态机，超出 v1 收益。列范围外。
