# FR-308: bot-worker runtime 下发部署 + 经全局包运行

> 状态：草拟　·　关联 PRD：FR-308　·　分支：feature/fr-308-bot-worker-deploy　·　关联 ADR：ADR-XXXX（占位，落地时统一分配真号；修订 ADR-006 的 bot-worker 分发假设）

## 1. 背景与目标

装机节点（install-worker.sh / deploy-worker.sh）上没有 bot-worker：Go worker spawn 假设 `bot-worker/dist/index.js` 相对 cwd 存在且其 426MB `node_modules` 就位——真机复现：node-2 只铺 dist 时 bot spawn `ERR_MODULE_NOT_FOUND: mineflayer`。目标：bot-worker **源码(dist)自动下发** + 依赖走**全局包**模型（用户拍板：worker 里只有全局包），bot spawn 经 `NODE_PATH` 消费全局 node_modules——装个 mineflayer 插件（全局包）bot 立即能用。

## 2. 需求与范围

- **dist 自动下发**：CP 构建期内嵌 bot-worker/dist（同 install-scripts/worker 内嵌范式），Worker 在 bot spawn 前发现本地 dist 缺失/指纹不符时自动从 CP 拉取解压（自愈式，装机脚本零改动、老节点升级 worker 后同样自动获得）。
- **NODE_PATH 注入**：bot spawn 环境带 `NODE_PATH=<数据根>/opt/runtimes/global/node_modules`（FR-307 的全局目录），使全局装的 mineflayer/插件可被 import。
- **依赖预检**：spawn 前检查 `mineflayer` 在全局 node_modules 可达；缺失时 bot 启动失败信息**明确引导**「到节点全局包管理页安装 mineflayer 与 mineflayer-pathfinder」（不自动安装——装包动作统一走 FR-307 语义，保持 307/308 并行零依赖）。
- **范围外**：不改 install-worker.sh（自愈下发已覆盖装机场景）；不自动装依赖（307 的 UI/端点负责）；bot-worker 不 bundle（保持 tsc dist + 全局依赖模型）。

## 3. 设计

### 3.1 CP 内嵌与分发端点

- 构建期 `make embed-botworker`：`bot-worker/dist` 打成 `bot-worker.tar.gz` + sha256 清单，go:embed 进 CP（目录 `.gitignore` 占位，未注入优雅降级——端点 404，Worker 保持现状）。`make dist`/`build` 接入该步。
- 新端点 `GET /worker-assets/bot-worker.tar.gz`（鉴权复用 worker-assets 既有 token 语义）+ `GET /worker-assets/bot-worker.manifest`（sha256+构建版本，Worker 比对指纹决定是否重拉）。

### 3.2 Worker 自愈下发

- bot spawn 前（`ensureBotWorker`）：目标 `<数据根>/opt/bot-worker/`（**迁离 cwd 相对路径**，纳入数据根；`JIANMANAGER_BOT_WORKER_PATH` 显式指定时跳过自愈保兼容）。
  - 本地无 dist 或 `manifest.sha256` 与 CP 清单不符 → 从 CP 拉 tar.gz 校验 sha256 后解压（原子：临时目录+rename；复用 jdk 包 DownloadAndExtract 语义含 symlink 支持）。
  - CP 不可达/未内嵌 → 若本地已有 dist 用旧的；全无 → spawn 报错引导。
- `BotWorkerPath` 解析顺序：env 显式 > 数据根自愈目录 > 旧相对路径（向后兼容开发环境）。

### 3.3 NODE_PATH 与依赖预检

- spawn env 追加 `NODE_PATH=<runtimesRoot>/global/node_modules`（与 FR-307 全局目录约定一致；307 未落地/目录不存在时照样注入——路径不存在 node 会忽略）。
- 预检：`<global>/node_modules/mineflayer` 目录存在即视为可达；缺失 → `Start` 返回错误「bot 依赖未安装：请到节点『全局包管理』安装 mineflayer 与 mineflayer-pathfinder」（该错误经 bot 状态/日志面向用户）。

### 3.4 ADR-XXXX（修订 ADR-006）

记录：bot-worker 分发模型=CP 内嵌 dist + Worker 自愈拉取（数据根 opt/bot-worker）+ 依赖全局包 NODE_PATH，取代「dist 相对 cwd + node_modules 随仓」的开发态假设；ADR-006 的 stdin/stdout IPC 与 Node 子进程边界不变。

## 4. 任务拆分

- [ ] make embed-botworker + CP go:embed + 分发/manifest 端点（未注入降级）
- [ ] Worker ensureBotWorker 自愈（指纹比对/下载校验/原子解压/路径解析顺序）
- [ ] bot spawn NODE_PATH 注入 + mineflayer 预检 + 引导性错误
- [ ] ADR-XXXX 落稿（占位号）
- [ ] 测试：指纹比对/降级/路径解析单测（httptest 假 CP）、NODE_PATH 注入与预检单测、CP 端点单测
- [ ] 文档同步：ARCHITECTURE（分发模型+目录）、API.md（新端点）、PRD 状态、CHANGELOG 尾行

## 5. 验收标准

- 单测：自愈下发（无 dist→拉取；指纹符→跳过；CP 不可达→用旧/报错）、NODE_PATH 注入、预检缺失引导。
- **真机（node-2，联合 FR-307 整合后）**：装机节点无 bot-worker → worker 自愈拉 dist → 面板全局包页装 mineflayer+mineflayer-pathfinder（FR-307 功能）→ 建 bot → **bot 真入 MC 服**（日志 `nodeSource=managed-scan` + 不再 ERR_MODULE_NOT_FOUND）。
- 真机项需用户确认；单测全绿不替代。

## 6. 风险 / 待定

- mineflayer 全局装的传递依赖体积（~200MB+）——走 npmmirror（FR-306 已配）可接受；磁盘占用一次性。
- NODE_PATH 对 ESM `import` 的支持：Node 的 ESM 解析默认不吃 NODE_PATH——**关键取舍**：bot-worker 是 `type: module`（ESM）！需改用 `--experimental-... `？否——正确做法：spawn 时把 cwd 或包解析根指向全局目录（如 `node --preserve-symlinks` 无关）；可靠方案=在 bot-worker dist 同级放一个 `node_modules` **junction/symlink → 全局 node_modules**（自愈下发时创建），ESM/CJS 解析都天然命中，零 Node 参数。spec 定此方案，NODE_PATH 仅作 CJS 兜底同时注入。
- 施工中若发现 symlink 方案在某平台受限（Windows 非特权），回退方案=Windows 用 junction（mklink /J，无需特权）；实现按 GOOS 分派。
