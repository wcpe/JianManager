# JianManager 部署指南

面向运维：如何用发布产物部署并运行 JianManager。开发构建见仓库根 [README.md](../README.md)。

## 1. 产物与组件

发布包（`jianmanager-vX.Y.Z-<os>-<arch>.tar.gz` / `.zip`）解压后含：

| 文件 | 说明 |
|---|---|
| `control-plane`(`.exe`) | Control Plane：唯一面向浏览器的 HTTP 入口，已内嵌前端 UI（单二进制） |
| `worker`(`.exe`) | Worker Node：受 Control Plane 通过 gRPC 调度，管理本机游戏服进程、终端、指标、Bot |
| `control-plane.yml` / `worker.yml` | 配置样例 |
| `README.md` / `DEPLOY.md` / `CHANGELOG.md` | 文档 |

三进程模型：`浏览器 →(HTTP/WS)→ Control Plane →(gRPC)→ Worker Node →(spawn)→ Bot Worker(Node.js)`。
Bot Worker 是 Node.js 子进程，其 dist 已内嵌 Control Plane，Worker 注册后**自动下发**到本机，无需手动准备文件（FR-308，见 §6）。

## 2. 环境要求

- Control Plane / Worker：对应平台的可执行文件即可运行，无需额外运行时（纯 Go 静态二进制，内置 SQLite）。
- 运行 Minecraft 等游戏服：目标 Worker 机需要对应 JDK。现代 Paper（1.18+）需 Java 17/21——可在节点托管便携 JDK（见 §7），无需系统预装。
- Bot 功能：Worker 机需要 Node.js 20+——可在节点「运行时」页一键安装托管 Node，无需系统预装（见 §6）。
- 数据库：默认 SQLite（零依赖）；生产可切 MySQL（见 §4）。

## 3. 快速部署（单机）

同一台机器跑 Control Plane + 一个 Worker。

```bash
# 1. Control Plane（HTTP :8080，gRPC :9100）
export JIANMANAGER_JWT_SECRET="$(openssl rand -hex 32)"   # 务必设置；CP 与 Worker 必须一致
./control-plane
#   首次启动后浏览器打开 http://<本机IP>:8080，按引导创建管理员（§5）

# 2. Worker Node（另开终端；gRPC :9101，WS 终端 :9102）
export JIANMANAGER_JWT_SECRET="<与上面相同>"
export JIANMANAGER_CONTROL_PLANE_GRPC="127.0.0.1:9100"     # Control Plane 的 gRPC 地址
export JIANMANAGER_HOST="127.0.0.1"                        # 浏览器/CP 回拨本 Worker 的地址（见 §8）
./worker
```

Worker 启动即向 Control Plane 注册，前端「节点」页应显示在线 + 实时 CPU/内存/磁盘。

## 4. Control Plane 配置

读取顺序：当前目录 `control-plane.yml` / `configs/control-plane.yml`（`.yml` 优先、找不到回退 `.yaml`，FR-224），再被 `JIANMANAGER_` 前缀环境变量覆盖（`.` → `_`）。

```yaml
server:
  host: 0.0.0.0
  port: 8080          # 浏览器访问端口（API + UI）
  dev_mode: false
grpc:
  port: 9100          # Worker 连接的 gRPC 端口
database:
  driver: sqlite      # sqlite | mysql
  dsn: data/jianmanager.db   # MySQL 示例: user:pass@tcp(host:3306)/jianmanager?charset=utf8mb4&parseTime=True
jwt:
  secret: CHANGE_ME   # 必改；签用户登录会话（自 FR-275 起不再需要与 Worker 同步）
  # ws_secret: 留空即可——CP↔Worker WS 令牌密钥（终端/探针桥，FR-275/ADR-061）：生产未配时
  # 自动生成并持久化到数据根 etc/ws-token-secret.key，经注册/心跳自动下发给 Worker，零配置可用。
  # 显式配置仅用于过渡兼容（如设为旧 jwt.secret 值以兼容未升级的 Worker）。
  access_ttl: 15m
  refresh_ttl: 168h
log:
  level: info         # debug|info|warn|error
  format: json        # json|text
file_version:         # 通用文件改前快照（FR-051）
  max_per_file: 20    # 单文件保留版本上限，超出删最旧；<=0 不限制
  max_size_bytes: 5242880  # 触发快照的单文件大小上限（默认 5MiB），超过则跳过；<=0 不限制
```

常用环境变量覆盖：

| 环境变量 | 对应配置 | 默认 |
|---|---|---|
| `JIANMANAGER_SERVER_PORT` | server.port | 8080 |
| `JIANMANAGER_GRPC_PORT` | grpc.port | 9100 |
| `JIANMANAGER_DATABASE_DRIVER` | database.driver | sqlite |
| `JIANMANAGER_DATABASE_DSN` | database.dsn | data/jianmanager.db |
| `JIANMANAGER_JWT_SECRET` | jwt.secret | dev-secret-change-me（**必改**；只签用户会话，不再下发 Worker） |
| `JIANMANAGER_JWT_WS_SECRET` | jwt.ws_secret | 空（生产自动生成持久化 + 注册自动下发 Worker；仅过渡兼容时显式配，FR-275/ADR-061） |
| `JIANMANAGER_DATA_DIR` | 数据根（资产/缓存） | 进程目录下 `data/` |
| `JIANMANAGER_CLIENT_KEY_ENC_SECRET` | client_dist.key_enc_secret | 空（env 优先；未配时生产自动生成并持久化，dev 兜底；不阻断建密钥，见下） |

> **拉取密钥可逆加密密钥（FR-192，见 ADR-044）**：让管理员可在频道页**查看拉取密钥明文 + 复制**（解决「忘记密钥只能轮换→已分发玩家集体断更」）。鉴权仍只用哈希比对、行为不变；另存一份 AES-256-GCM 可逆加密副本。密钥来源优先级为：`JIANMANAGER_CLIENT_KEY_ENC_SECRET`（32 字节 base64，生成示例 `openssl rand -base64 32`）环境变量注入优先；生产未配置时自动生成并持久化到数据根 `etc/client-key-enc.key`；`dev_mode: true` 时使用内置开发密钥兜底。自动生成或持久化失败时优雅降级为密钥不可查看，拉取密钥创建、改值与鉴权不被阻断。注：更换此密钥后旧加密副本解不开（按「不可查看」处理，不崩；密钥版本化为后续事项）。
>
> **客户端分发信任模型（FR-087，见 ADR-054）**：当前统一为 HTTPS 传输 + 拉取密钥鉴权 + sha256 完整性校验；manifest 不再签名，也不再需要配置签名私钥。验签 / `JIANMANAGER_CLIENT_SIGN_PRIVKEY` / Ed25519 签名私钥相关方案已由 ADR-054 废弃，不作为现行部署项。

## 5. 首次启动引导

首次访问 `http://<cp>:8080` 会进入引导页，创建首个管理员账号（FR-017）。完成后用该账号登录。无需预置 bootstrap 配置。

## 6. Worker Node 配置

Worker 全部用环境变量配置（也可放 `worker.yml` 同名键）：

| 环境变量 | 说明 | 默认 |
|---|---|---|
| `JIANMANAGER_CONTROL_PLANE_GRPC` | **必填**，Control Plane gRPC 地址 `host:9100` | 无 |
| `JIANMANAGER_JWT_SECRET` | 旧 CP（< FR-275）兼容项：与 CP 一致才能过终端/探针桥校验。新 CP 会在注册时自动下发 WS 令牌密钥（持久化到 `etc/node-identity.json`），**无需配置** | dev-secret-change-me |
| `JIANMANAGER_NODE_NAME` | 节点显示名 | node-01 |
| `JIANMANAGER_NODE_UUID` | 固定节点 UUID（留空则首次生成；重装后想复用既有节点须显式指定） | 自动 |
| `JIANMANAGER_HOST` | 浏览器/CP 回拨本 Worker 的地址（终端/WS）。留空自动探测本机 IP；NAT/容器下需显式指定可达地址 | 自动 |
| `JIANMANAGER_GRPC_PORT` | Worker gRPC 端口 | 9101 |
| `JIANMANAGER_WS_PORT` | Worker 终端 WebSocket 端口 | 9102 |
| `JIANMANAGER_DATA_DIR` | 数据根（游戏服在 `var/servers/`、JDK 在 `opt/jdks/`） | 进程目录下 `data/` |
| `JIANMANAGER_BOT_WORKER_PATH` | 显式覆盖 Bot Worker 入口 `index.js` 路径（指定后固定该入口、不再自愈下发；仅仓库式部署等特殊场景需要，见下） | 留空自动解析（数据根自愈副本优先） |
| `JIANMANAGER_DISABLE_JDK` | 设为 `1` 关闭托管 JDK 能力 | 启用 |
| `JIANMANAGER_JDK_TEMURIN_BASE` | 一键安装 Temurin 的下载基址（国内可换镜像） | `https://api.adoptium.net` |
| `JIANMANAGER_JDK_CORRETTO_BASE` | 一键安装 Corretto 的下载基址 | `https://corretto.aws` |
| `JIANMANAGER_JDK_ZULU_BASE` | 一键安装 Zulu 的元数据 API 基址 | `https://api.azul.com` |

### Bot Worker（Node.js，自动下发）

Bot 功能由 Node.js 子进程承载。bot-worker 的 dist 已内嵌 Control Plane 二进制（FR-308，见 ADR-070）：Worker 注册成功后经 gRPC（`FetchBotWorkerArchive`）自动拉取并物化到 `<数据根>/opt/bot-worker/`（sha256 校验 + 原子换入；指纹一致跳过；CP 不可达时回退本地已有副本，只告警不阻断启动）。**发布包部署无需手动准备 bot-worker 文件**，只需在 Worker 机备好两项运行时：

1. **Node.js 20+**：推荐在节点「运行时」页一键安装托管 Node（FR-299）；Worker spawn 时优先用本机扫描到的最高版 Node，无候选回退 PATH 中的 `node`（FR-300）。
2. **Bot 运行时依赖**（`mineflayer`、`mineflayer-pathfinder`）：不随归档分发，在节点「全局包管理」安装（FR-307）。缺装时 Bot 启动会返回明确的安装指引，不会裸崩。

入口解析顺序：`JIANMANAGER_BOT_WORKER_PATH` 显式覆盖（指定后不再自愈下发）> 数据根自愈副本 > 仓库相对路径 `apps/bot-worker/dist/index.js`（旧布局 `bot-worker/dist/index.js` 兼容）。

**仓库式部署回退（按源码运行）**：从仓库检出运行 Worker 时，可自行构建并显式指定入口：

```bash
cd apps/bot-worker
npm install
npm run build          # 产出 dist/index.js
# 然后在 Worker 进程设置：
export JIANMANAGER_BOT_WORKER_PATH="$(pwd)/dist/index.js"
```

### 终端 / 探针监控 401 排查（FR-275/276，见 ADR-061）

终端页显示「**终端令牌被节点拒绝**……WS 令牌密钥与平台不一致」（而非普通「连接已断开」）时，按序核对：

1. **节点是否已升级并重启**：新版 Worker 在注册时自动获取 WS 令牌密钥（心跳持续自愈），重启节点即可；CP 与 Worker 建议同批升级。
2. **手动/旧版部署**：旧版 Worker 仍用本地 `jwt_secret` 校验——将其设为与 CP 一致；或在 CP 显式设 `jwt.ws_secret` 为旧 `jwt.secret` 值过渡（逃生口），待节点升级后移除。
3. **CP 密钥被轮换过**（改了 `jwt.ws_secret` 或删了数据根 `etc/ws-token-secret.key`）：在网 Worker 会经心跳 ≤1 拍自愈；**已下发的探针桥长期 token 会失效**，需对受影响实例走「探针在线更新」（FR-068）或重建服重发探针配置，插件桥监控才能恢复。
4. 普通「连接已断开/连接 Worker 失败」是网络类问题（端口不可达、实例未运行），与密钥无关，按网络与端口章节排查。

## 7. JDK 托管（运行现代 Minecraft 服）

现代 Paper 需 Java 17/21。可让 Worker 托管便携 JDK（无需系统装 Java）：把解压好的 JDK 放到 Worker 数据根 `opt/jdks/<名>/`，节点会扫描并登记；在「一键搭建」向导里为实例绑定该 JDK（向导默认绑定节点最高版本已装 JDK）。也可用系统 Java（向导 JDK 选「不指定」），但需自行保证版本匹配。

## 8. 网络与端口

| 端口 | 进程 | 谁访问 |
|---|---|---|
| 8080 | Control Plane HTTP/WS | 浏览器、运维 |
| 9100 | Control Plane gRPC | 各 Worker → CP |
| 9101 | Worker gRPC | CP → Worker |
| 9102 | Worker 终端 WebSocket | **浏览器 → Worker**（携带 CP 签发的一次性 token） |

要点：浏览器终端**直连 Worker 的 WS 端口（9102）**，不经 Control Plane 转发。多机/防火墙部署时，`JIANMANAGER_HOST` 必须是浏览器可达的 Worker 地址，且 9102 对浏览器放通。CP 与所有 Worker 的 `JIANMANAGER_JWT_SECRET` 必须一致，否则终端 token 校验失败。反向代理（HTTPS）时请透传 `X-Forwarded-Proto`，终端会据此选 `wss://`。

## 9. 升级与备份

- **升级**：停 Control Plane 与 Worker，替换二进制，重启。SQLite/MySQL 表结构由 GORM 自动迁移。daemon 方式启动的游戏服为脱离进程，Worker 重启会经 PID 文件重连恢复，不影响在运行的游戏服。
- **备份**：定期备份 Control Plane 数据库（SQLite 即 `data/jianmanager.db` 文件）与各 Worker 数据根 `var/servers/`（游戏存档）。

## 10. 多节点

Control Plane 一台，Worker 多台：各 Worker 设相同 `JIANMANAGER_CONTROL_PLANE_GRPC`（指向同一 CP）与相同 `JIANMANAGER_JWT_SECRET`，不同 `JIANMANAGER_NODE_NAME`。注册后在「节点」页统一可见、可分别调度实例。
