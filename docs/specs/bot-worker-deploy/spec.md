# FR-308: bot-worker runtime 下发部署 + 经节点受控包根运行

> 状态：草拟　·　关联 PRD：FR-308　·　分支：feature/fr-308-bot-worker-deploy　·　关联 ADR：ADR-072（取代 ADR-070；保留其对 ADR-006 分发假设的修订）

## 1. 背景与目标

装机节点（install-worker.sh / deploy-worker.sh）上没有 bot-worker：Go worker spawn 假设 `bot-worker/dist/index.js` 相对 cwd 存在且其 426MB `node_modules` 就位——真机复现：node-2 只铺 dist 时 bot spawn `ERR_MODULE_NOT_FOUND: mineflayer`。目标：bot-worker **源码(dist)自动下发** + 依赖走**节点受控包根**模型（UI/API 仍称全局包，表示节点共享；包管理器不再用真全局模式），bot spawn 经 ESM 可见的 `node_modules` 链接消费依赖——装个 mineflayer 插件后 bot 立即能用。

## 2. 需求与范围

- **dist 自动下发**：CP 构建期内嵌 bot-worker/dist（同 install-scripts/worker 内嵌范式），Worker 注册成功后发现本地 dist 缺失/指纹不符时自动从 CP 拉取解压（自愈式，装机脚本零改动、老节点升级 worker 后同样自动获得）；spawn 热路径不做网络请求。
- **Node 运行时门槛**：bot-worker 只使用 Node.js `>=22.13.0`。NodeResolver 对显式配置、托管扫描候选与 PATH `node` 都执行真实 `--version` 探测；显式配置无效时直接报错，托管候选按探测到的完整版本排序，PATH 也必须过最低版本；只缓存成功解析。
- **依赖解析根**：受控 dist 同级 `node_modules` 链接优先指向 `<数据根>/opt/runtimes/global/node_modules`（ADR-072 的节点受控项目根），但仅在 mineflayer 与 mineflayer-pathfinder 均已安装时切换；否则旧 `<global>/lib/node_modules` 依赖完整时继续兼容。每次 spawn 前刷新链接，spawn 环境仍带按「新在前、旧在后」拼接的 `NODE_PATH`，但它仅作 CJS 兜底，ESM 以链接为主通道。
- **依赖预检**：链接刷新后，只检查入口脚本目录向上的 `node_modules` ESM 祖先链（覆盖 dist 同级与仓库旧布局）；不得因祖先链外的裸节点受控根或旧兼容根依赖完整而直接放行。缺失时 bot 启动失败信息**明确引导**「到节点全局包管理页安装 mineflayer 与 mineflayer-pathfinder」（不自动安装——装包动作统一走 FR-307 语义）。
- **范围外**：不改 install-worker.sh（自愈下发已覆盖装机场景）；不自动装依赖（307 的 UI/端点负责）；bot-worker 不 bundle（保持 tsc dist + 节点受控依赖根模型）。

## 3. 设计

### 3.1 CP 内嵌与分发通道

- 构建期 `make embed-botworker`：`bot-worker/dist` 打成**确定性** `bot-worker.tar.gz`（含 `package.json` 保 ESM 语义）+ sha256 清单，go:embed 进 CP（目录 `.gitignore` 占位，未注入优雅降级，Worker 保持现状）。`make dist` 接入该步。
- **分发通道修订（施工定案，见 ADR-072，继承 ADR-070）**：初稿的「HTTP `GET /worker-assets/bot-worker.tar.gz` 复用既有 token 语义」不可实现——worker-assets 的 token 是一次性 enroll 语义，撑不住每次启动的常态自愈。改为新增 unary RPC `FetchBotWorkerArchive`（CP 侧实现，`node_uuid+node_secret` 与重注册同源鉴权；请求携带本地 `known_sha256`，指纹一致回空归档省流）。归档 ~25KB 远低于 64MiB 单消息上限（FR-305），且 gRPC 通道天然复用反向隧道（ADR-066），NAT 节点零额外暴露面。不再新增 HTTP 端点。

### 3.2 Worker 自愈下发

- 自愈时机（施工定案）：**注册成功后一次**（`botdist.Ensure`，60s 超时，失败只告警不阻断启动），而非每次 bot spawn 前——CP 刚服务完注册必可达，且避免 spawn 热路径带网络往返。目标 `<数据根>/opt/bot-worker/`（**迁离 cwd 相对路径**，纳入数据根；`JIANMANAGER_BOT_WORKER_PATH` 显式指定时跳过自愈保兼容）。
  - 本地无 dist 或 `.jm-manifest.json` 指纹与 CP 不符 → 经 RPC 拉归档、sha256 复核后解压（原子：临时目录+rename，旧目录挪 `.old` 可回滚；归档只含常规文件，解压拒路径穿越）。
  - CP 不可达/未内嵌 → 若本地已有物化副本用旧的；全无 → Ensure 报错、bot 入口沿用旧相对路径兜底。
- `BotWorkerPath` 解析顺序：env 显式 > 数据根自愈目录 > 旧相对路径（向后兼容开发环境）。

### 3.3 Node 解析、依赖链接与预检

- NodeResolver 保持「显式配置 > 托管扫描 > PATH」优先级，但三路都必须真实执行 `--version`。显式配置探测失败或低于 `22.13.0` 时拒绝启动；托管候选忽略扫描结果里可能陈旧的 major/version，按重新探测的 major/minor/patch 完整排序；PATH `node` 同样探测。失败解析不缓存，成功解析才缓存，spawn 失败后的 Refresh 会清缓存重探。
- 主通道=**node_modules 链接**（ADR-072）：自愈时在 `opt/bot-worker/node_modules` 建链接 → 节点受控根 `global/node_modules`（Windows 用 junction，其余用 symlink）。每次受控 dist spawn 前再次刷新：新根两项 Bot 依赖完整时选新根，否则旧 `global/lib/node_modules` 依赖完整时继续选旧根，两者都不完整时默认新根并由预检报错；已存在的非空实体目录不覆写（保留用户现场）。
- spawn env 追加 `NODE_PATH=<新受控根>:<旧兼容根>`（按平台路径分隔符拼接，CJS 兜底；目录不存在时 Node 会忽略）。
- 预检（链接刷新后）：沿入口脚本目录逐级向上检查 `node_modules`，mineflayer 与 mineflayer-pathfinder 必须在 ESM 祖先链中的**同一目录**完整命中才放行；祖先链外的裸新根或旧根不直接参与放行。缺失或分散安装 → `Start` 返回错误「bot 依赖未在同一目录完整安装：请到节点『全局包管理』重新安装 …（缺少 X）」。

### 3.4 ADR-072（取代 ADR-070，继续修订 ADR-006 的分发假设）

记录：bot-worker 分发模型继续为 CP 内嵌 dist + Worker 经 gRPC 自愈拉取（数据根 opt/bot-worker）；依赖模型从 npm/pnpm 真全局安装改为 `<runtimes>/global` 受控普通项目，`package.json` 合并安全 overrides，node_modules 链接主通道与 NODE_PATH 兜底保留。它继续取代「dist 相对 cwd + node_modules 随仓」的开发态假设；ADR-006 的 stdin/stdout IPC 与 Node 子进程边界不变。

## 4. 任务拆分

- [x] make embed-botworker（确定性归档+manifest）+ CP go:embed（未注入降级）
- [x] proto `FetchBotWorkerArchive` + CP handler（身份鉴权/指纹省流/降级应答）
- [x] Worker botdist.Ensure 自愈（指纹比对/sha256 复核/原子解压/node_modules 链接/路径解析顺序）+ main 接线
- [x] bot spawn 前受控链接刷新 + NODE_PATH 注入 + ESM 可见路径预检 + 引导性错误
- [x] ADR-072 落稿并取代 ADR-070
- [x] 测试：自愈全路径单测（fake client）、链接功能性验证、预检/NODE_PATH 单测、CP RPC 鉴权与嵌入态双分支单测
- [x] 文档同步：ARCHITECTURE（分发模型+目录+RPC）、PRD 状态、CHANGELOG 尾行（无新增 HTTP 端点，API.md 不涉）
- [x] 真机（2026-07-13）：node-2 自愈拉 dist（CP journal「下发 bot-worker 归档 25267B」+ worker「dist 已更新 211fb4fd6c45」，重启复验指纹省流零重拉；win-node junction 同验）→ 面板全局包装 mineflayer-pathfinder 2.4.5（任务 19 succeeded）→ UI 一键搭建 Paper 1.21.8（bot-arena2 @25566）→ UI 建 bot → **MC 日志 `fr308bot joined the game`**、bot status=connected、`nodeSource=managed-scan`，无 ERR_MODULE_NOT_FOUND

## 5. 验收标准

- 单测：自愈下发（无 dist→拉取；指纹符→跳过；CP 不可达→用旧/报错）、Node 三来源真实探测与最低版本、完整版本排序/成功缓存、spawn 前新旧根链接刷新、NODE_PATH 注入、ESM 可见路径预检。
- **真机（node-2，联合 FR-307 整合后）**：装机节点无 bot-worker → worker 自愈拉 dist → 面板全局包页装 mineflayer+mineflayer-pathfinder（FR-307 功能）→ 建 bot → **bot 真入 MC 服**（日志 `nodeSource=managed-scan` + 不再 ERR_MODULE_NOT_FOUND）。
- 真机项需用户确认；单测全绿不替代。

## 6. 风险 / 待定

- mineflayer 安装到节点受控根后的传递依赖体积（~200MB+）——走 npmmirror（FR-306 已配）可接受；磁盘占用一次性。旧 `global/lib/node_modules` 不自动删除，迁移期可能短暂双份占用。
- NODE_PATH 对 ESM `import` 的支持：Node 的 ESM 解析默认不吃 NODE_PATH——**关键取舍**：bot-worker 是 `type: module`（ESM）！可靠方案是在 bot-worker dist 同级放一个 `node_modules` **junction/symlink → 节点受控根**（自愈下发时创建），ESM/CJS 解析都天然命中，零 Node 参数。spec 定此方案，NODE_PATH 仅作 CJS 兜底同时注入。
- 施工中若发现 symlink 方案在某平台受限（Windows 非特权），回退方案=Windows 用 junction（mklink /J，无需特权）；实现按 GOOS 分派。
