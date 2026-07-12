# ADR-070: bot-worker dist 自愈下发与依赖解耦

- **日期**: 2026-07-13
- **状态**: accepted（修订 ADR-006：「Bot 必须经 Node.js 子进程 + stdin/stdout IPC」不变；修订其隐含前提「bot-worker dist 随仓库检出就位于工作目录」——dist 改为 CP 内嵌构建产物、Worker 经 gRPC 自愈拉取，运行时依赖改由 FR-307 托管全局包提供）
- **上下文**: FR-308。Worker 经一键安装独立部署（无仓库检出）时，`bot-worker/dist/index.js` 相对路径不存在，bot 能力整体不可用；即便手工拷 dist，mineflayer 等依赖也无处解析（bot-worker 为 ESM，`type: module`，NODE_PATH 对 ESM 无效），子进程以 `ERR_MODULE_NOT_FOUND` 裸崩、用户只能翻 stderr 取证。需要决策分发通道、物化布局、依赖解析三件事。
- **决策**:
  1. **CP 内嵌 + gRPC 下发**：`make embed-botworker` 把 `bot-worker/dist` 打成**确定性 tar.gz**（路径排序、固定 mtime，同内容同指纹；~25KB）连同 `manifest.json`（version/sha256/size）注入 `internal/controlplane/embed/botworker/`（不入库，`.gitignore` 占位，未注入运行时优雅降级）。Worker 注册成功后持 `node_uuid+node_secret` 调新增 unary RPC `FetchBotWorkerArchive`（CP 侧实现，与重注册同源鉴权）拉取。**放弃 spec 初稿的「复用 worker-assets HTTP token」**：那是一次性 enroll 语义，无法支撑每次启动的常态自愈；gRPC 通道天然复用注册/心跳的鉴权与反向隧道（ADR-066），NAT 节点零额外暴露面，25KB 远低于 64MiB 单消息上限（FR-305），单 unary 足够。
  2. **指纹比对 + 原子物化**：Worker 上报本地 `.jm-manifest.json` 的 sha256，与 CP 内嵌指纹一致时 CP 不回字节（省流）；不一致回全量归档，Worker sha256 复核后解压到临时目录、`rename` 原子换入 `<dataroot>/opt/bot-worker/`（旧目录先挪 `.old` 可回滚）。归档携带 `package.json`（`type: module`）保住解压后 `.js` 的 ESM 语义。入口解析顺序：`JIANMANAGER_BOT_WORKER_PATH` 显式覆盖（不自愈）> 数据根物化副本 > 旧相对路径 `bot-worker/dist/index.js`（仓库式部署向后兼容）。CP 不可达/未内嵌一律回退本地已有，只告警不阻断 Worker 启动。
  3. **依赖走 FR-307 托管全局包，ESM 靠链接**：mineflayer / mineflayer-pathfinder **不随归档分发**（体积大、版本宜独立升级），由用户在节点『全局包管理』安装。ESM 解析靠 dist 同级 `node_modules` **链接 → 托管全局 node_modules**（Windows 用 junction `mklink /J` 无需特权，其余平台 symlink；npm 全局布局按平台取 `global/node_modules` 或 `global/lib/node_modules`）；NODE_PATH 仅作 CJS 兜底注入 spawn 环境。spawn 前**依赖预检**：dist 同级 / 旧布局上级 / 托管全局候选任一命中即放行，缺失时返回「请到节点『全局包管理』安装 mineflayer 与 mineflayer-pathfinder」的可操作指引，而非让子进程裸崩。
- **理由**:
  - 内嵌 + 版本同源（`--version $(VERSION)`）延续 ADR-062 的「CP 随身自带同版本资产」思路，dist 与 CP 一次构建同进同出，不存在版本漂移。
  - 依赖不打包进归档：mineflayer 带原生依赖树数十 MB，打进 CP 二进制不可接受；托管全局包已有完整的安装/升级/卸载 UI 与任务链路（FR-307），复用即可。
  - junction 而非 NODE_PATH 作主通道：ESM 规范不读 NODE_PATH，链接是唯一不改 bot-worker 源码的解法；预检把「缺依赖」从崩溃取证问题变成一句面板指引。
- **后果**:
  - CP 升级后 Worker 需重启才拉到新 dist（自愈时机=注册后一次）；bot-worker 迭代频率低，可接受，后续需要可挂心跳指纹广播。
  - pnpm 全局布局（`PNPM_HOME` 独立目录）暂不在链接目标内，bot 依赖需经 npm 安装；面板默认 npm，预检报错也会引导。
  - 手工在 `opt/bot-worker/node_modules` 放过实体目录的，自愈不覆写（保留用户现场），链接不建但预检仍按实体目录放行。
