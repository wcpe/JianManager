# FR-306 / FR-307: 节点包管理器 + 多 registry 配置 + 全局包可视化管理

> 状态：草拟　·　关联 PRD：FR-306（本 FR，地基）/ FR-307（依赖 306）　·　分支：feature/fr-306-node-pkg-mgmt
> 波1 只实现 FR-306；FR-307 依赖 FR-306 落 dev 后并行开（波2，与 FR-308 同批）。本 spec 一份覆盖两条。

## 1. 背景与目标

FR-298~303 已建节点运行时库（JDK/Node.js 扫描、安装、聚合）。Node 生态要真用起来（bot-worker 靠 mineflayer、未来插件），节点上需要：① 选包管理器（npm 默认，pnpm/yarn 可选）；② 配下载源（CN 直连官方 registry 慢，需 npmmirror 镜像 + 私有 `@scope` 源）；③ 可视化管全局包（FR-307）。

**FR-306 目标**（本 FR）：节点级包管理器偏好（corepack 激活 pnpm/yarn）+ 多 registry 配置的读写与落盘，作为 FR-307（全局包 UI）/FR-308（bot-worker 依赖装全局）的地基。

**明确不做（YAGNI）**：per-project 包管理（用户拍板「worker 里只有全局包」）；npm 账号登录/publish；registry 健康探测（配了就用，失败在装包时报）。

## 2. 需求

- **包管理器选择**：节点级选 `npm` / `pnpm` / `yarn`（默认 npm）。pnpm/yarn 经 **corepack**（Node 16.9+ 自带，FR-299 装的 Node 22 含）`corepack enable <pm>` 激活指定版本，零额外下载。
- **多 registry 配置**：
  - 默认 registry（`registry=<url>`，如 npmmirror `https://registry.npmmirror.com`）。
  - `@scope` 域源（`@myco:registry=<url>`，多条）。
  - 可选凭据（`//host/path/:_authToken=<token>`），API 响应/日志**脱敏**（复用 `httpclient.Sanitize`/`maskSecret` 语义）。
- 配置落节点**托管 .npmrc**（PM 操作经 `NPM_CONFIG_USERCONFIG` 指向它，不污染用户 `~/.npmrc`）。
- 面板节点页「运行时」区加「包管理器」子区：PM 选择器 + registry 列表编辑（增删行，name/url/scope/token）。
- **不在本 FR**：全局包的 list/install/remove/update（FR-307）。

## 3. 设计（FR-306）

### 3.1 数据模型（CP）

新表 **`node_pm_configs`**（节点级单例，node_id 唯一）：
```
id / node_id(uniqueIndex) / pm(varchar16 default 'npm': npm|pnpm|yarn)
registries(text, JSON 数组：[{name,url,scope?,token?}]) / created_at / updated_at
```
token 入库存明文（节点级配置，与 proxy.url 同密级），**API 出参与日志脱敏**。

### 3.2 托管 .npmrc 布局

- 全局配置目录：`<数据根>/opt/runtimes/`（与 nodejs-<major> 平级），托管 `.npmrc` 落此。
- 生成规则：`registry=<默认>` + 每个 scope 一行 `@<scope>:registry=<url>` + 有 token 的源写 `//<host><path>/:_authToken=<token>`。
- PM 操作（FR-307）统一带 `NPM_CONFIG_USERCONFIG=<该 .npmrc>`（npm/pnpm/yarn 均尊重此 env 或等价）。

### 3.3 Worker 新 RPC（proto 追加，紧跟 InstallRuntime 之后）

- `GetPMConfig(node) → {pm, registries[]{name,url,scope,tokenMasked bool}, corepackAvailable bool, pmVersion string}`
  - 探测：托管 Node 的 bin 下有无 `corepack`；当前激活 PM 版本（`<pm> --version`，经托管 node 的 PATH）。
- `SetPMConfig(pm, registries[]) → {ok, pmVersion, error?}`
  - pm≠npm：用托管 Node 跑 `corepack enable <pm>`（在托管 node bin 目录建 shim；失败回报，不静默）。
  - 写托管 `.npmrc`（原子写：临时文件+rename）；token 原样写入。
  - 校验：pm 枚举合法、registry url 是 http(s)、scope 命名合法（`@` 开头）。

### 3.4 CP 端点与服务

挂 `/nodes/:id`（平台管理员 + 审计）：
- `GET /nodes/:id/pm-config` → 代理 Worker GetPMConfig，融合 DB 持久值；registry token 出参脱敏（`tokenMasked=true` + 不回明文）。
- `PUT /nodes/:id/pm-config {pm, registries[]}` → 校验 → Worker SetPMConfig → 落 `node_pm_configs`（upsert）→ 审计 `node.pm.config`（中英 i18n 随身，FR-303 守则）。节点离线 503。
  - registry token 更新语义：出参脱敏后前端回传可能是掩码——**掩码值表示「不改该源 token」**，明文表示更新（同 proxy.url 保存语义）。

### 3.5 前端

节点页「运行时」区（FR-298 的 NodeRuntimeSection）加「包管理器」子区：
- PM 单选（npm/pnpm/yarn，显当前激活版本；选 pnpm/yarn 提示将 corepack enable）。
- registry 列表：默认源一行（url）+ scope 源多行（scope/url/token）+ 增删行；保存走 PUT。
- 遵守 ui-modals（若用弹窗编辑则套 scrollable-dialog；行内编辑列表不受限）。

## 4. 任务拆分（FR-306）

- [ ] `node_pm_configs` 表 + model + AutoMigrate
- [ ] proto：GetPMConfig/SetPMConfig RPC + Worker 实现（corepack enable + .npmrc 原子写 + 探测）
- [ ] CP：pm-config GET/PUT 端点 + service（DB upsert + Worker 代理 + 脱敏 + 审计）
- [ ] 前端：节点页「包管理器」子区（PM 选择 + registry 编辑）+ i18n（含 audit.actions.node.pm.config 中英）
- [ ] 测试：Worker corepack/.npmrc 生成单测（伪 node bin）、脱敏/掩码保存语义单测、端点单测（fake worker）、前端 DOM 测
- [ ] 文档同步：ARCHITECTURE（新表+RPC）、API.md、PRD 状态、CHANGELOG 尾行

## 5. 验收标准（FR-306）

- 真机（node-2，已装 Node 22）：设 pm=pnpm → `corepack enable` 成功、`pnpm --version` 可用；设 registry=npmmirror → 托管 `.npmrc` 含 `registry=https://registry.npmmirror.com`；GET 回显 registry token 脱敏。
- 掩码保存语义：回传掩码 token 不清空源 token（单测锁）。
- 各真机项需用户确认；单测全绿不替代。

## 6. FR-307（全局包可视化管理，依赖 FR-306，波2 施工）

- **全局目录**：`<数据根>/opt/runtimes/global`（PM global prefix 指向；`npm i -g --prefix`/pnpm `--global-dir`/yarn 等价），与 .npmrc 同 userconfig。
- **Worker RPC**：`ListGlobalPackages → [{name,version,latest?}]`（`<pm> ls -g --json` 解析）；`InstallGlobalPackage(name,version?)` / `RemoveGlobalPackage(name)` / `UpdateGlobalPackage(name)` —— 装/更新走**任务中心异步**（复用 FR-290 停滞看门狗 + FR-279 网络失败引导 + FR-291 语义），经 FR-306 的 PM+.npmrc。
- **CP 端点**：`GET/POST/DELETE /nodes/:id/global-packages`（+ update）；审计 `node.pkg.{install,remove,update}`（i18n 随身）。
- **前端**：全局包管理页（列表+搜索+版本+可更新标记+增删改），走已配 registry。
- **验收（真机）**：装 `mineflayer-pvp` 全局 → 列表可见（走 npmmirror）→ 更新 → 删除消失。
- FR-308 消费：bot-worker 依赖即装为全局包，bot spawn 带 `NODE_PATH=<global>/node_modules`（FR-308 spec）。

## 7. 风险 / 待定

- corepack `enable` 需写 node bin 目录——托管 Node 目录可写（我们装的），OK；若节点手动登记的外部 Node 只读则 corepack enable 可能失败，回报「PM 激活失败，请用 npm 或托管 Node」。
- pnpm/yarn 的 global dir 与 .npmrc 尊重方式略有差异——FR-307 施工时按 PM 分派（本 FR 只落 .npmrc + corepack，不碰 global 安装）。
- proto/worker.proto 与并行会话（FR-281/304/305）同文件演进——RPC 追加文件尾段，整合时 make proto 重生成收敛。
