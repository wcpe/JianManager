# FR-302: 导入现有服务器向导（节点级，就地接管 / 搬迁托管区）

> 状态：草拟　·　关联 PRD：FR-302　·　分支：feature/fr-302-import-server　·　关联 ADR：ADR-XXXX（占位，落地时统一分配真号；落地 ADR-007 预留的「导入已有目录」高级模式，修订其「工作目录一律系统分配」为「导入例外」）

## 1. 背景与目标

运营商上手面板前往往已有跑着/存量的服务器目录。现在只能新建实例再手工搬文件。目标：面板向导选中节点上的现成目录 → 自动探测 → 一键收编为受管实例。ADR-007 当年已预留「用户自填工作目录 — 保留为『导入已有目录』高级模式，非默认」，本 FR 落地它并补 ADR 记录决策。

## 2. 需求

- 节点级入口（节点页/实例列表「导入现有服务器」）。
- 浏览节点目录选根（复用 FR-178 BrowseDir）。
- Worker 探测：核心 jar 候选、内嵌 JDK、`server.properties` 端口、eula 状态。
- 导入模式二选一（向导内选）：
  - **就地接管**：工作目录=原目录（托管区外例外）；**删除实例绝不删原目录**。
  - **搬进托管区**：目录整体移入系统分配的 `servers/<slug>-<shortid>`（同盘 rename 优先，跨盘拷贝+清源）。
- 登记实例（结构化启动 ADR-008：jdk + jvm 参数 + jar）；探到的内嵌 JDK 可勾选登记进节点 JDK（`node_jdks`，managed=false 外部登记语义）。
- 范围外（不做）：批量导入多目录、非 MC generic 进程导入、导入时自动改端口冲突（探测结果展示端口，冲突由用户自行改）。

## 3. 设计

### 3.1 Worker 新 RPC（proto 追加）

- `InspectServerDir(path) → {jars[]{path,size,mainClassHint}, jdks[]{path,vendor,version,arch}, serverPort, eulaAccepted, propsFound}`
  - jar 候选：目录下 `*.jar`（深度≤2），`server.jar`/`paper-*`/`purpur-*`/`spigot-*`/`fabric-server*`/`forge-*` 排前；Main-Class 嗅探（MANIFEST）标 hint。
  - JDK 候选：`jre*/`、`jdk*/`、`runtime/`、`java/` 子目录经 `jdk.detectAt` 探测。
  - `server.properties` 读 `server-port`；`eula.txt` 读 `eula=true`。
  - 守卫：path 必须绝对且存在、目录；**不得指向托管区内已有实例目录**（防重复收编）。
- `ImportServerDir(path, mode, targetSlug) → {workDir, moved}`（仅 migrate 模式做实际搬迁：同盘 `os.Rename`，跨盘递归拷贝+校验数量/字节后清源；in_place 模式 no-op 回原 path）。

### 3.2 CP 端点与模型

- `POST /instances/import/inspect {nodeId, path}` → 代理 InspectServerDir（平台管理员；审计 `instance.import.inspect`）。
- `POST /instances/import {nodeId, path, mode:in_place|migrate, name, jarPath, jdkId?, registerJdkPaths?[], memoryMb?, onlineMode?}` →
  1. mode=migrate：调 ImportServerDir 得最终 workDir；in_place：workDir=path。
  2. 建 Instance（Type=minecraft_java、ProcessType=daemon、结构化启动同 provision 路径；端口沿用探测值不改文件）。
  3. `registerJdkPaths` 逐个登记 node_jdks（managed=false）。
  4. 审计 `instance.import`（detail 含 mode/path）。
- **模型**：`instances` 加列 `work_dir_in_place bool default false`（就地导入标记）。
- **删除语义（关键守则）**：就地实例删除时 CP 明确传「跳过目录删除」；且 Worker 现有托管区守卫（`remove_instance_ops.go`：托管区外一律跳过 RemoveAll）已天然兜底——双保险，spec 要求补一条端到端测试锁死。
- 就地实例 UI 上带「就地导入」徽章 + 删除确认文案明示「不会删除原目录」。

### 3.3 ADR-XXXX（占位）

记录：导入实例的工作目录例外（就地=托管区外合法存在）、删除不碰就地目录守则、migrate 的 rename/拷贝策略；标注修订 ADR-007（其「保留为高级模式」的预留正式落地）。

### 3.4 前端向导

模态向导（ui-modals 纪律，scrollable-dialog 壳）：选目录（复用目录浏览组件）→ 探测结果页（jar 单选、JDK 勾选登记、端口/eula 展示）→ 模式选择（就地/搬迁，含各自后果说明）→ 名称/内存/JDK 绑定 → 提交，成功跳实例页。

## 4. 任务拆分

- [ ] proto：InspectServerDir / ImportServerDir + Worker 实现（探测器 + 搬迁器 + 守卫）
- [ ] CP：inspect/import 端点 + service + `work_dir_in_place` migration + 删除跳过语义
- [ ] 前端：导入向导模态（节点页/实例列表入口）
- [ ] ADR-XXXX 落稿（占位号）
- [ ] 测试：探测器单测（伪目录布局：paper 服/嵌套 jre/无 props）、搬迁器单测（同盘/跨盘/清源校验）、就地删除不删目录端到端测试、端点单测、前端 DOM 测
- [ ] 文档同步：ARCHITECTURE（RPC+模型列）、API.md、PRD 状态、CHANGELOG 尾行

## 5. 验收标准

- 真机（node-2）：植入一个现成 paper 服务器目录（含 server.jar + server.properties + eula）→ 向导探测出 jar/端口 → **就地接管**导入 → 实例启动到 `Done` → 删除实例 → **原目录完好无损**。
- 真机：同构目录**搬迁模式**导入 → 目录出现在托管区系统分配路径、源位置清空 → 启动到 `Done`。
- 探测守卫：指向托管区内已有实例目录被拒；不存在路径 4xx。
- 各真机项需用户确认通过；单测全绿不替代。

## 6. 风险 / 待定

- 跨盘搬迁大目录耗时——V1 同步执行（提交后转任务中心可后续增强，不阻塞本 FR）；向导提示大目录建议就地。
- 探测 Main-Class 嗅探对 shaded jar 可能误标——仅作排序 hint，最终 jar 由用户单选拍板。
- proto 同文件与并行会话演进冲突——追加尾段，整合时重生成收敛。
