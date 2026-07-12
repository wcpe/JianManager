# FR-298~301: 节点运行时库（多运行时泛化 / 自动扫描 / Node.js 安装器 / Bot 接管 / 聚合刷新）

> 状态：草拟　·　关联 PRD：FR-298 / FR-299 / FR-300 / FR-301　·　分支：feature/fr-298-runtime-library（波1）+ 波2 三分支
> 波 1 仅实现 FR-298；FR-299/300/301 依赖 FR-298 落 dev 后并行开（波 2）。本 spec 一份覆盖四条的设计契约。

## 1. 背景与目标

面板目前只认 JDK（`node_jdks` + 安装/登记/Probe），且登记靠手动填单一路径；bot-worker 裸 `exec("node")` 依赖 PATH，新节点没配就起不来。目标：把「节点上装了什么运行时」泛化管理（JDK / Node.js，Python 预留类型）、自动扫描发现、缺 Node 可一键装、CP 聚合可视化。

**明确不做（YAGNI / 已被用户否决）**：CP 不沾运行时本体——不扫 CP 主机、不存归档、不代拉、不做 CP 分发中转；「不出外网重复下载」由已交付的出站代理链路（ADR-043 下发 + FR-289~292）解决。Python 只预留 `type` 枚举值，不做安装器（代码无消费者）。

## 2. 需求

- **FR-298**（波1）：运行时类型抽象 + Worker 扫描常见路径 RPC + 扫描候选勾选入库 + 手动登记泛化 + 节点页「运行时」分区。
- **FR-299**（波2）：Node.js 一键安装（nodejs.org dist 便携归档），下载链路语义与 JDK 齐平（FR-289 arch 归一 / FR-290 停滞看门狗+网络归类 / FR-291 残骸自愈 / FR-292 删除顶层清理）。
- **FR-300**（波2）：bot-worker spawn 优先托管/登记 Node，回退 PATH。
- **FR-301**（波2）：CP 聚合缓存多运行时矩阵 + 上次同步时间 + 手动刷新。

## 3. 设计

### 3.1 数据模型（CP）

- **`node_jdks` 不动**：实例外键（`instances.jdk_id`）、既有安装/同步全链路保持零变更。
- **新表 `node_runtimes`**（承载非 JDK 类型；JDK 不迁移）：
  ```
  id / node_id / type(varchar16: nodejs|python…) / name(展示名,如 "Node.js 22")
  version(varchar64, 如 "22.17.0") / major(int) / arch / path / managed(bool)
  created_at / updated_at；唯一约束 (node_id, type, path)
  ```
- 聚合视图层把 `node_jdks`（type=jdk）与 `node_runtimes` 拼成统一 Runtime 视图（type/vendor|name/major/version/arch/path/managed），**只在读侧拼**，写侧各走各表。

### 3.2 Worker 侧（FR-298 / 299）

- **新 RPC `ScanRuntimes(ScanRuntimesRequest{types[]}) → {candidates[]}`**（追加在 worker.proto JDK 段之后；候选=type/vendor/version/major/arch/path/already_registered）。扫描路径表（存在才探，探测失败静默跳过）：
  - jdk（linux）：`/usr/lib/jvm/*`、`/opt/java*`、`/opt/jdk*`、`~/.sdkman/candidates/java/*`；（windows）：`Program Files\Java\*`、`Program Files\Eclipse Adoptium\*`、`Program Files\Microsoft\jdk*`；探测复用 `jdk.detectAt`（bin/java -XshowSettings）。托管根下的（已在库）标 `already_registered`。
  - nodejs（linux）：`/usr/local/bin/node`、`/usr/bin/node`、`/opt/node*/bin/node`、`~/.nvm/versions/node/*/bin/node`；（windows）：`Program Files\nodejs\node.exe`、`%APPDATA%\nvm\v*\node.exe`；探测 `node --version`（输出 `vX.Y.Z`）+ `process.arch` 经 `node -p process.arch`。
- **FR-299 Node 安装器**：`runtime.Manager`（新包 internal/worker/runtime 或并入 jdk 包泛化——施工时定，倾向新包复用 jdk 包的 download 基建导出）：
  - 版本解析：`https://nodejs.org/dist/index.json`（含 lts 字段）；镜像可配（平台设置 `runtime.mirror.nodejs`，默认官方；npmmirror 等可换）。
  - 归档：linux `node-vX.Y.Z-linux-x64.tar.gz` / windows `node-vX.Y.Z-win-x64.zip`；arch 归一复用 FR-289 语义（x64/arm64——注意 nodejs 命名是 x64/arm64，与 adoptium 的 x64/aarch64 不同，归一化表按类型分派）。
  - 托管目录：`<数据根>/opt/runtimes/nodejs-<major>/`；下载经与 JDK 同一 outbound provider + `downloadAndExtractWithProgress`（FR-290 停滞看门狗）+ 残骸自愈（FR-291 完成标记 = `bin/node`|`node.exe`）+ 删除顶层清理（FR-292 同款守护，托管根 `opt/runtimes`）。
  - 任务中心异步（复用 jdk_install 的 task 模式，kind=`runtime_install`）。

### 3.3 CP 端点（FR-298 / 299 / 301）

挂 `/nodes/:id` 下（平台管理员）：
- `POST /nodes/:id/runtimes/scan {types?}` → 代理 Worker ScanRuntimes，回候选列表（节点离线 503）。
- `GET /nodes/:id/runtimes` → 统一 Runtime 视图（node_jdks + node_runtimes 拼装；含同步语义同 JDK List 的 syncFromWorker 容忍）。
- `POST /nodes/:id/runtimes` → 登记（type=jdk 落 node_jdks 走现链路；其它落 node_runtimes）。
- `POST /nodes/:id/runtimes/install {type:nodejs, major, arch}` → 异步装（202+taskId，FR-299）。
- `DELETE /nodes/:id/runtimes/:rid?type=` → 删除（托管的连文件，语义同 JDK；外部登记只删记录）。
- `GET /runtime-assets/overview` 扩展（FR-301）：response 增 `runtimes` 多类型矩阵 + `syncedAt`；新 `POST /runtime-assets/refresh` 强制全节点 syncFromWorker。
- 审计 action（随身带 i18n 翻译，FR-303 守则）：`node.runtime.scan` / `node.runtime.register` / `node.runtime.install` / `node.runtime.delete`。

### 3.4 bot-worker 接管（FR-300）

- Worker spawn Bot 时解析 node 可执行：节点运行时库内 type=nodejs 的最高 major（或 bot 配置显式指定 runtime id）→ 绝对路径；无命中回退 `"node"`（PATH，现行为）。解析结果与来源打进 bot 启动日志。
- 配置：worker.yml 不加新项（真相源=CP 库存）；bot 级可选字段 `nodeRuntimeId` 进 bot 配置（V1 可只做「自动选最高版」，显式指定列为可选任务）。

### 3.5 前端

- 节点页 JDK 面板扩「运行时」分区（沿用现 JDK 面板范式）：分区列表承载**非 JDK 类型**（类型徽章区分；JDK 由上方 JDK 面板富列表唯一呈现——v0.15.0 验收修正：分区列表再含 type=jdk 会整页双列重复，统一视图保留在 API 层 `GET /nodes/:id/runtimes`）+「扫描发现」按钮（模态列候选勾选入库，jdk 候选照常参与，遵守 ui-modals 纪律）+「安装 Node.js」入口（版本选择，任务进度跳任务中心）。
- 运行时资产页（FR-301）：JDK 矩阵扩多类型 + 「上次同步 <相对时间>」+ 刷新按钮。

## 4. 任务拆分

波1（FR-298）：
- [ ] `node_runtimes` 表 + model + migration
- [ ] proto：ScanRuntimes RPC + Worker 实现（jdk/node 探测器 + 路径表）
- [ ] CP：scan/list/register/delete 端点 + service（统一视图拼装）+ 审计
- [ ] 前端：节点页「运行时」分区 + 扫描模态
- [ ] 测试：探测器单测（伪目录布局）、端点单测、前端 DOM 测
- [ ] 文档同步：ARCHITECTURE（数据模型+RPC）、API.md、PRD 状态、CHANGELOG 尾行

波2（FR-299/300/301 各自任务见各 FR，此处不展开；施工前按本 spec §3 对应小节）。

## 5. 验收标准

- FR-298：真机（node-2）植入 JDK 与 Node 假安装目录 → 扫描列出候选 → 勾选入库 → 列表可见；重复扫描标 already_registered；未知类型拒绝。
- FR-299：真机 node-2 经出站代理装 Node LTS → `bin/node --version` 对版；停滞/残骸/删除语义单测齐平 FR-290~292。
- FR-300：真机把节点 PATH 无 node 场景（或以库内 node 优先证据）拉起 bot 成功，启动日志标来源。
- FR-301：真机运行时资产页矩阵显全节点 JDK+Node；手动刷新即时；断一个 worker 刷新容忍显旧。
- 各真机项需用户确认通过；单测全绿不替代。

## 6. 风险 / 待定

- proto/worker.proto 与另一并行会话（FR-281 隧道）存在同文件演进——新 RPC 追加在文件尾段，整合时以重生成 pb 收敛。
- Windows 扫描路径含空格/权限差异——探测失败静默跳过，不阻断扫描整体。
- nodejs arch 命名（arm64）与 adoptium（aarch64）不同——归一化表按 type 分派，勿共用。
