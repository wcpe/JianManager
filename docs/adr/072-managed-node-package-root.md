# ADR-072: 节点包改用受控项目依赖根

- **日期**: 2026-07-17
- **状态**: accepted（取代 [ADR-070](070-bot-worker-dist-selfheal.md)；保留其 CP 内嵌 dist、Worker 经 gRPC 自愈、数据根原子物化与 ESM `node_modules` 链接决策，仅取代 npm/pnpm 真全局安装模型）
- **上下文**: FR-307/FR-308。ADR-070 将 bot-worker 的 mineflayer 依赖交给节点「全局包管理」，并用 `<数据根>/opt/runtimes/global` 作为包管理器全局前缀。真全局安装存在两个长期问题：一是 npm/pnpm 的平台布局与全局语义不同，需要在 `global/node_modules`、`global/lib/node_modules`、`PNPM_HOME` 间分支；二是真全局安装不稳定消费项目级 `overrides`，无法在不降级 mineflayer 的前提下定向收敛其传递依赖安全漏洞。节点需要一个仍由平台独占管理、但遵循普通项目依赖解析与锁定规则的统一根。
- **决策**:
  1. **保留 ADR-070 的分发与进程决策**：bot-worker dist 仍由 Control Plane 构建期内嵌，Worker 注册后经 `FetchBotWorkerArchive` 自愈拉取，sha256 复核后原子物化到 `<数据根>/opt/bot-worker/`；入口解析、CP 不可达时使用本地副本、Node.js 子进程与 stdin/stdout JSON IPC 均不变。
  2. **节点包根改为受控普通项目**：`<数据根>/opt/runtimes/global` 不再作为 npm/pnpm 的真全局前缀，而是平台独占管理的普通 Node 项目根。其依赖统一落在 `<runtimes>/global/node_modules`，节点 UI/API 中的「全局包」仍表示「对该节点全部 Bot/运行时消费者可见」，不表示包管理器的 `--global` 模式。
  3. **受控 `package.json` 原子合并**：每次包操作前确保根目录存在 `package.json`。若文件已存在，必须保留 `dependencies` 与所有未知字段，只合并平台拥有的 `private: true`、`overrides` 与 `pnpm.overrides`；写入采用同目录临时文件加 rename。平台安全覆盖至少包含 `@azure/msal-node>uuid=11.1.1` 与 `yggdrasil>uuid=11.1.1`，npm 与 pnpm 使用相同定向规则，禁止用宽泛顶层覆盖改变无关依赖。
  4. **包管理命令使用本地项目语义**：npm 的 install/ls/outdated/uninstall 均带 `--prefix <root>`，不再带 `--global`；pnpm 的 add/list/outdated/remove 均带 `--dir <root>`，不再依赖 `PNPM_HOME` 或全局目录。registry 仍由托管 `.npmrc` 与 `NPM_CONFIG_USERCONFIG` 提供。yarn 因缺少本范围内可验证的一致受控语义继续明确拒绝。
  5. **ESM 链接在每次受控 dist spawn 前刷新**：bot-worker 的 ESM 主通道仍是在 dist 同级建立 `node_modules` 链接。自愈物化时先建一次，每次受控 dist spawn 前再按当前依赖完整性刷新；只有新 `<runtimes>/global/node_modules` 同时包含 mineflayer 与 mineflayer-pathfinder 时才从完整旧 `<runtimes>/global/lib/node_modules` 切换。两者都不完整时默认新根并由启动预检报错；`NODE_PATH` 候选固定新在前、旧在后且仅作 CJS 兜底。
  6. **预检只认可 ESM 实际可见路径**：链接刷新后，启动预检沿入口脚本目录逐级向上检查 `node_modules` 祖先链，两项依赖必须在同一目录完整命中。祖先链外的裸新根或旧根即使依赖完整也不得直接放行，避免入口仍链接旧根时误判可运行。
  7. **Node 三来源真实探测并强制最低版本**：NodeResolver 保持「显式配置 > 托管扫描 > PATH」优先级，但三路都执行真实 `--version` 探测并只接受 `>=22.13.0`。显式配置无效时直接报错；托管候选按重新探测的完整 major/minor/patch 排序，不信任扫描结果中的陈旧版本字段；PATH `node` 同样必须过门槛。解析失败不缓存，只有成功结果缓存。
  8. **Bot 仓库与节点根使用同一安全约束**：`apps/bot-worker` 声明 Node.js `>=22.13.0`，提供生产依赖审计脚本，并以相同的两个定向 override 生成精确 lockfile。不得通过 `npm audit fix` 或降级 mineflayer 消除告警，避免安全修复悄然改变 Bot 协议能力与运行行为。`pkgmgr` 独立拥有受控项目根语义，不依赖 `botdist`；`botdist` 只按文件系统约定消费该根。
- **理由**:
  - 普通项目安装天然具有稳定的单一 `node_modules` 布局，Windows/Linux 共享同一当前路径，减少平台分支与 pnpm 全局目录差异。
  - 项目级 `overrides`/`pnpm.overrides` 能把安全约束纳入依赖求解；定向覆盖只替换两条已知 uuid 传递边，不强迫 mineflayer 降级，也不影响依赖树中其他 uuid 消费者。
  - 保留已有 `dependencies` 与未知字段，使平台可接管此前已安装包而不破坏现场扩展；原子写避免 Worker 中断时留下半份 JSON。
  - 保留旧 `lib/node_modules` 候选，让升级后的 Worker 能继续启动既有 Bot；后续任一包变更会在新受控根安装，迁移无需一次性搬运或删除旧目录。
  - 继续采用 ESM 可见的文件系统链接，而不是依赖 `NODE_PATH`，保持 ADR-070 已验证的模块解析行为。
- **后果**:
  - FR-307 的公开名称与 RPC/API 可保持「Global」兼容，但实现语义变为节点级受控项目；运维文档必须避免把它描述为 npm/pnpm 真全局安装。
  - 首次包操作会创建或合并 `<runtimes>/global/package.json`；已有依赖不会被清空，平台字段会被规范化为安全基线。
  - 旧 `<runtimes>/global/lib/node_modules` 不自动删除，可能暂时占用额外磁盘；它仅作运行兼容候选，不再接收新安装。
  - 新旧目录同时存在时，受控 dist 每次 spawn 前重新判定；仅在新根依赖完整后切换，新根不完整而旧根完整时继续沿用旧根。启动预检只信任刷新后的 ESM 可见路径，并要求 mineflayer 与 mineflayer-pathfinder 位于同一根。
  - Node.js 最低版本提升到 22.13.0；旧 Node 20 节点需先在运行时页升级。显式路径、托管扫描与 PATH 回退都不能绕过门槛，临时探测失败会在下次解析重试而不是被缓存。
- **被否方案**:
  - **继续真全局安装，仅在仓库 lockfile 修复**：节点真实运行时不会消费仓库 lockfile，无法达成运行时零 moderate。
  - **运行 `npm audit fix`**：会允许依赖求解器扩大改动面，可能降级或重排 mineflayer 依赖，不符合定向根修复要求。
  - **顶层强制所有 uuid 为同一版本**：影响范围超过已知漏洞路径，可能破坏仍依赖旧 uuid API 的其他包。
  - **把全部 node_modules 打进 CP 归档**：显著放大二进制与下发体积，并把依赖升级绑定到 CP 发版，违背 dist 与运行时依赖解耦目标。
