# 功能规格：探针托管配置编排与对账

> 状态：开发中（本地实现完成 + 真机联调通过，未提交、未发版）　·　关联 PRD：FR-560　·　分支：待定

## 1. 背景与目标

ServerProbe 在托管模式下不再读本地 `config.yml` 作为权威配置，而是由控制面经插件桥下发（协议见
`docs/specs/probe-managed-mode/spec.md`）。上游「探针托管协议地基」那次提交已落地**探针侧**的协议基础能力：

- 探针身份文件、`config_push` 应用与自检回退、`config_applied` 回执、`config_state` 周期上报；
- CP 侧 `managed_mode.go` / `managed_mode_frames.go` / `managed_mode_tiers.go` /
  `probe_managed_assembly.go`：身份文件生成、协议帧构造、66 键三档分级、Worker 指标快照接收。

缺的是**运营面**：没有地方保存三层配置，没有灰度下发编排，没有回执消费，没有对账读模型，也没有页面。
本规格补的就是这一段——让"托管配置"从协议能力变成运营可用的功能。

目标用户：管理多节点 Minecraft 服群的管理员（运维视角关心"这 200 台机器现在跑的是不是我要的那份配置"）。

## 2. 需求（要什么）

- 范围内：
  - 三层配置存储（global / node / instance），点分键扁平模型，按 scope 优先级合并；
  - 保存前预检（禁档键整包拒绝、危险档提示）与影响预演（受影响实例数、指标量倍率、危险档键）；
  - 灰度下发：会话 → 目标 → 批次，逐批人工放行，ACK 超时收敛，失败率熔断；
  - 回执消费：`config_applied` 定位目标并落终态，`config_state` 落"每实例最新状态"；
  - 桥连接态消费：`connected` / `disconnected` 作为矩阵连接维度的主证据；
  - 对账矩阵读模型：期望版本 / 观测版本 / 一致性判定 / 连接态 / 配置来源 + 汇总与下钻；
  - 回滚：重推上一次 completed 会话的**冻结快照**，需原因 + 二次确认；
  - 前端两页：配置编辑与下发页、对账矩阵页。
- 不做（范围外）：
  - 探针侧的配置应用、自检、回退语义（上游已实现，本规格只消费其结果）；
  - 自动回滚（配置推错最多让指标不准，自动回滚会在无人决策下再次改动全平台）；
  - 节点级覆盖之外的新作用域维度（如分组作用域）；
  - Beacon 相关能力（由其他会话负责）。

## 3. 设计（怎么做）

### 3.1 数据模型

`internal/controlplane/model/managed_config.go`，四张表（均在 `AutoMigrate` 注册）：

| 表 | 模型 | 语义 |
|---|---|---|
| `managed_config_layers` | `ManagedConfigLayer` | 三层配置层，`(scope, scope_ref_id)` 唯一，带乐观并发 `version` |
| `managed_push_sessions` | `ManagedPushSession` | 下发会话：冻结的 values/removals/逐键来源 + 批次/熔断/超时参数 |
| `managed_push_targets` | `ManagedPushTarget` | 逐实例目标：状态机 + 基线版本 + 回执结果 |
| `managed_instance_config_states` | `ManagedInstanceConfigState` | 每实例最新状态：观测版本、生效值、连接态（自报 + 桥判定） |

### 3.2 合并与分级

- 合并只在**扁平点分键**上进行（与协议 `values` 逐字一致）。每层先展平，避免 `aggregation`
  与 `aggregation.window` 变成互不相干的键。
- 分级复用上游 `ManagedConfigTierOf`（66 键三档）与 `IsForbiddenConfigPath`（18 个禁档键），
  适配层收敛在 `managed_tier_guard.go`——**不重新定义键表**，否则两处规则会漂移。
- 禁档键整包拒绝（不是过滤掉继续推）：探针本来也会拒，CP 提前拒是为了让错误出现在保存时而不是下发后。

### 3.3 下发编排

状态机：`pending → pushing → applied | rejected | failed | timeout`，`skipped` 为非错误终态。

关键设计点：

- **版本双层语义**：`session.ConfigVersion` 是会话级单调递增（CP 的代数）；`baseVersion` 取自探针
  上一次 `config_applied` 回报的版本（探针自己的序号）。两者不可互换——混用会让探针整包拒绝。
- **不等待**：下发用 `SendPluginCommand(wait=false)`。探针的应用可能跨多个 tick，同步等待会把它
  拖成 gRPC 超时，于是"已应用"被误报成"未回执"。
- **逐批人工放行**：探针配置推错会影响采集精度甚至资源占用，自动推进会让"先看一批效果"变成空话。
- **熔断**：分母是已终结目标（不含 skipped）——离线跳过不是"配置推坏了"。
  熔断落 **`failed`（终态），不是 `paused`**：两者只差一个 status，但可恢复性相反——
  `paused` 能被 Resume 一键续推剩余批次，而触发熔断的那份配置很可能就是问题本身，
  续推等于把已验证的坏配置扩散到更多机器。熔断现场（`break_reason`/`break_threshold`/
  `break_observed`）必须留存，运维据此判断是配置问题还是这批机器环境问题。
- **进程重启收口**：推进循环由内存 goroutine 驱动，进程重启后它已消失而库里的会话仍是
  `running`——没人推进、`expireStaleTargets` 只扫 `pushing` 碰不到、`Release` 又要求
  `awaiting_release`，人工也放行不了。故启动时必须把 `running` 收口为 `paused`
  （`pause_kind=restart`），给运维留一条 Resume 通道。`awaiting_release` 不动：它本就是
  静止态，改动反而丢掉"已推完一批、该看效果了"的进度事实。
- **回滚重推的是"当时发出去的内容"**：回滚会话从源目标继承 `pushed_payload_json`（下发时留存
  的载荷原文），而非按当前层重新合并——两次推送之间只要有人改过 global 层，重新合并出的就是
  一份从未在任何机器上生效过的配置，"回到上一次推送的状态"也就不成立。
  注意只复用**内容**（values/removals）：`configVersion` 必须是回滚会话新铸的版本、
  `baseVersion` 必须是该实例当前真实基线；原样复用旧版本号会让探针按旧版本回执、
  而 CP 正按新版本等它，回执永远对不上，最终全批超时。
  源目标从未真正下发过（`pending`/`skipped`）时载荷为空，此时按现场重新合并——
  与"没推过的东西无所谓回到哪个状态"一致。

### 3.4 回执与状态消费

复用 `StreamPluginEvents` 消费链路（另起订阅会重复消费同一条长驻流）：
`PlayerEventService` 分流 `config_applied` / `config_state` / `connected` / `disconnected`
到 `ManagedPushService` 与 `ManagedInstanceStateService`，钩子在事件读循环里同步调用，
落库必须切 goroutine，否则会卡住该节点所有实例的实时事件。

**载荷提取是本规格踩过两次坑的地方**（详见 §6）。

### 3.5 对账矩阵

单条 SQL 用两个 LEFT JOIN（每实例最近目标 + 最新状态）把一切判完，分页与总数都交给数据库：
1000+ 实例时不把整表读进内存。一致性四态：

| 判定 | 条件 |
|---|---|
| `failed` | 最近目标状态 ∈ {failed, rejected, timeout} |
| `neverReported` | 状态行不存在，或 `config_version IS NULL` |
| `inSync` | `COALESCE(期望, 0) = 观测版本` |
| `drift` | 其余（有观测且不等） |

`null` 与 `0` 全程严格区分：`desiredVersion` 用 null 表示"从未下发"（0 是首次托管前的合法版本号）；
`cacheAgeSeconds` 用 null 表示"从未成功应用"（0 会被误读成"刚刚生效过"）。

### 3.6 接口与页面

- 后端：`internal/controlplane/router/managed_config.go`（键目录 / 配置层 CRUD / 校验 / 影响预演 / 下发 /
  推进 / 放行 / 暂停 / 回滚 / 矩阵 / 汇总 / 下钻），在 `router.go` 注册，`apps/control-plane/main.go` 装配。
- 前端：
  - `src/pages/ProbeManagedPage.tsx` —— 总控页，单页 3 Tab。
  - `src/pages/ManagedConfigPage.tsx`（配置编辑 + 下发编排）、`ManagedMatrixPage.tsx`（矩阵），
    由总控页复用而非重写。
  - `src/components/console/ManagedConfigEditor.tsx` / `ManagedConfigField.tsx` —— 可视化编辑器。
  - API 封装在 `src/api/managedConfig.ts` / `managedMatrix.ts`。

### 3.7 可视化配置编辑

**问题**：66 个点分键靠手写 JSON，用户既记不住键名，也看不出取值范围；写错了要到下发后
被探针拒绝才知道。

**做法**：新增 `GET /managed-configs/catalog` 暴露每个键的编辑元数据
（类型 / 分组 / 档位 / 中文名 / 说明 / 默认值 / 数值边界 / 可选值），前端按 `kind` 选控件：

| `kind` | 控件 |
|---|---|
| `bool` | 开关 |
| `int` / `float` | 数字输入（带 `min`/`max` 与单位） |
| `string` | 文本框；有 `options` 时改渲染下拉 |
| `stringList` | 标签列表（增删） |
| `object` | 结构化编辑器（`alert.rules` 用专用规则编辑器） |

**关键约束**：

- **元数据表与定档表必须键集合一致**（68 键双向比对，有单测）。缺一即两种缺陷：
  定档表有而元数据缺 → 界面上看不见，用户以为"不能配"；反之 → 能编辑却会被拒，用户白填。
- **`tier` 必须是英文枚举**（`forbidden`/`guarded`/`safe`），不能是中文。
  中文进协议字段会让调用方只能硬编码中文来比较——曾经的缺陷正是这样导致
  **禁止档键渲染出了可编辑控件**。
- **禁止档键列出但只读**：藏起来会让人以为"没有这个配置项"，而真相是"存在但不能远程改"。
- **增量语义要讲清**：本层只保存改过的键，没改的回退到低层。每字段区分
  「已设置」/「未设置但低层有值」/「用默认值」三态——混淆会让用户困惑于"我删了它为什么还在生效"。

**布局：左侧分类导航 + 右侧只显示当前分类 + 紧凑行**。

- **只显当前分类**：68 个字段平铺会滚很久，改成一次只看一个分类后页面长度与字段总数无关
  （实测页面文字量从 6526 降到 1092，约 1/6）；导航上标出该分类「已设置数/总数」。
- **编辑器必须占满整行**：早先配置页是 `lg:grid-cols-2` 两栏，编辑器被压进半栏（实测仅 540px），
  左导航再吃掉 160px，右侧字段只剩 368px，**字段全部横向溢出重叠**。
  配置页的主任务是改配置，编辑区应当优先拿到全部宽度；校验/预演/保存移到编辑器下方并排。
- **紧凑行而非卡片**：现代配置页惯例是「左标题+说明，右控件」。每字段一张大卡片时
  单字段约 200px 高、一屏只能看 5 个；改两栏行后单字段约 100px，一屏能看 7 个以上完整分类。

**搜索框跨分类过滤**：按**键名**（`collect-period-ticks`）、**中文名**（采集周期）或说明匹配。
搜索时隐藏分类树——结果是扁平列表，再显示分类会让人误以为命中项分属不同分类。
无命中时给出可操作提示（"试试键名片段或中文名"），而不是空白。

**兜底**：取不到 catalog 时回退到纯 YAML 编辑器（不阻塞使用）；可视化未覆盖的键仍可在
YAML 视图里改。

### 3.7.1 YAML 作为高级编辑格式（替代 JSON）

**为什么不是 JSON**：托管配置本身就是探针 `config.yml` 的增量，运维日常读写的也是 YAML。
高级编辑区用 JSON 等于要求在两套语法间来回翻译，且 `config.yml` 里本来有的行内注释会被抹掉。
改成 YAML 后「页面上看到的」与「探针文件里的」是同一套写法；服务端按 YAML 解析
（YAML 是 JSON 超集），历史 JSON 数据仍可直接提交，无需迁移。

**注释从权威目录生成，不是手写常量**：每个点分键的 `title` / `description` / `tier` /
`default` / `options` / `min`·`max` / `unit` 拼成注释行。目录新增一个键就自动带上注释，
不会出现「文档说这么写、校验说不行」的撕裂。序列化时**键集合只由配置决定，目录只决定
顺序与注释**——目录里有而本层没设置的键不输出，否则会把「没设置」变成「显式设为默认」，
从此再也回退不到低层。

**序列化必须处理多段键**：`alert.rules` 这类键要展开成 `alert:` → `rules:` 的父子层级。
曾把点分键当单层键直接写，嵌套键全部落进「目录未覆盖」分支，既没有注释、缩进也是错的。

**字符串一律加引号**：配置里有大量 `1s` / `on` / `yes` 这类会被 YAML 隐式转型的值
（`on` 会变成布尔真），不加引号等于悄悄改了配置的类型。

**实时校验与定位**：CodeMirror 6 + `@codemirror/lang-yaml`。解析失败时报错必须带
**行号与列号**（YAML 的失败几乎全是缩进造成的，只说"格式不正确"用户无从下手），
并把英文报错翻成中文（缩进、重复键、Tab 缩进、结构错位四类高频错误）。
同一层级重复键必须拦下——YAML 默认静默取后者，用户改了半天发现只有一处生效却毫无提示。

**往返保真**：可视化表单改值后不能整体重新序列化，那样会抹掉用户手写的注释、键顺序与空行。
实现上只重写**值确实变了**的那些行；结构变化（数组增删项、对象增删子键）才整段重建。

### 3.7.2 GitHub Settings 式版式与行内操作

**版式**：左栏一列可点的分类导航，右栏每个配置项占一行——标题与说明在左、控件在右，
操作按钮紧贴各自的控件，不压到页面底部。

**每个字段自带状态与写入按钮**：早先「校验 / 影响预演 / 保存」堆在页面最下方，
用户改完一个数字要滚到底才知道有没有问题。现在：

- **行内状态**：字段控件旁直接显示「通过 / 不通过」；不通过时具体原因贴在该字段下方。
  后端 `Validate` 返回的逐键拒绝原因按其中的键名匹配回具体字段，用户不必去底部对照清单。
- **行内「写入」**：只提交**这一个键**，不是保存整层。语义与「改草稿」明确区分，
  用户点一下就知道「已在草稿里」与「已经写进服务端」的差别。按钮只在该项有未落盘改动时可用。
- **底部只留一次性的「保存整层」**：逐字段写入之后仍需一次整体保存的场景（改了多处后一起提交），
  同时给出「N 项未落盘」计数，用户知道还有哪些没提交。

**边界判定必须在字段本地做**：目录里写着 `min: 20`，界面却要等后端才报错，
用户就会看到一个"合法"的 10——后端根本不管数值边界（它只管档位）。
故字段先做本地边界判定，外层结论（后端权威）优先覆盖。

### 3.8 页面结构与路由

三个动作（改配置 / 看对账 / 翻历史）原本各占一个一级导航入口，一级路由过多，
且完成一条链路要在三页间来回跳。

**收敛为单页 3 Tab，挂在两级路由 `/probe/managed`**：

| Tab | 内容 | 复用组件 |
|---|---|---|
| 配置与下发 | 写配置 → 影响预演 → 保存 → 建会话 → 推进 | `ManagedConfigPage(defaultTab="layers")` |
| 对账矩阵 | 期望 vs 观测、一致性判定、下钻 | `ManagedMatrixPage` |
| 下发历史 | 历史会话、失败明细、回滚 | `ManagedConfigPage(defaultTab="pushes")` |

- Tab 状态写进 URL query（`?tab=`），刷新与分享链接回到同一处；非法值回退到 `config` 而非白屏。
- 旧路径 `/managed-configs`、`/managed-matrix` 保留为**重定向**，既有收藏不失效。
- 复用而非重写：被收进来的两页各自已有完整实现与测试，总控页只做 Tab 编排。

## 4. 任务拆分

- [x] 四张表模型 + `AutoMigrate` 注册
- [x] 三层配置 CRUD、点分键合并、逐键来源、影响预演
- [x] 下发会话/目标/批次、ACK 超时、熔断、回滚
- [x] `config_applied` / `config_state` / 桥连接态消费
- [x] 对账矩阵读模型（列表 / 汇总 / 下钻）
- [x] HTTP 端点 + router 注册 + main 装配
- [x] 前端配置页与矩阵页（含 DOM 测试 21 + 12 例）
- [x] 假探针端到端（CP 编排 + ACK + 矩阵）
- [x] 真机联调（真 Paper 1.20.1 + 真 ServerProbe jar）
- [x] 可视化全量编辑（68 键分类导航 + 搜索 + 按类型控件 + 规则编辑器）
- [x] 高级编辑改为 YAML（CodeMirror + `@codemirror/lang-yaml`，注释由目录生成，见 §3.7.1）
- [x] GitHub Settings 式版式 + 字段行内校验与「写入」（见 §3.7.2）
- [x] 后端接受 YAML 输入（YAML 优先、JSON 兜底，数值归一为 int64）
- [x] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG
- [ ] 提交与版本归属（待用户决定）

## 5. 验收标准

单元 / 集成：

- [x] `go build ./...` 与 `go vet ./internal/controlplane/... ./apps/control-plane/` 通过
- [x] 托管相关 service / router 测试通过
- [x] 前端 `ManagedConfigPage` 21 例、`ManagedMatrixPage` 12 例全绿
- [x] 可视化编辑器 23 例、YAML 编解码 12 例、探针托管总控页 5 例全绿（见 §3.7）
- [x] 后端 YAML/JSON 双格式解析 4 例（含非对象拒绝、显式 null 删键）
- [x] 前端全量 3647 通过 / 2 失败（失败为干净基线已有的 i18n 用例，与本次改动无关）
- [x] 元数据表与定档表双向比对一致（68 键，缺一即失败）
- [x] 禁档键被 CP 预检拒绝（HTTP 400），危险档进入 `guardedKeys`

假探针端到端（`internal/e2e/probe_managed_e2e_test.go`）：

- [x] 三层写入 → 合并结果正确（instance 覆盖 global，global 独有键保留）
- [x] 首次下发 `baseVersion = 0`
- [x] 回 `config_applied(applied)` 后目标转 `applied`
- [x] `config_state` 上报后矩阵判 `inSync`，期望/观测版本均为真值
- [x] 探针断开后矩阵连接维度转 `disconnected`

页面布局（真实浏览器 1440 / 1024 / 768 三档实测）：

- [x] 导航项不溢出容器（`scrollWidth <= clientWidth`）
- [x] 分类导航与字段区横向重叠 0px
- [x] ≥1024px 左右布局、<1024px 转上下布局
- [x] 字段行内「标题列 / 控件列」不重叠
- [x] 页面无横向滚动（`body.scrollWidth == innerWidth`）

真机联调（`internal/e2e/probe_managed_real_test.go`，需 `MCV_E2E_PROBE_JAR`）：

- [x] 经 JM 一键搭建 Paper 服并部署真探针
- [x] 真探针连入插件桥，CP 观察到 `connected`
- [x] 真探针回 `applied`，矩阵 `inSync`、`mode=managed`
- [x] 探针自报生效值与下发值一致（`collect-period-ticks=40`）
- [x] 探针日志确认零监听：`metrics` / `mcp` 本地端口已跳过

## 6. 风险 / 待定

### 已发现并修复的三个真实缺陷（均为联调暴露，非静态审查可得）

1. **`AutoMigrate` 漏注册**：四个模型未注册，写配置直接 500 `no such table`。
2. **载荷解析错位**：探针把结构化字段放在 `payload` 段，CP 早期把**整帧**当载荷解析，
   于是 `configVersion` 恒为 0、`result` 恒为空串——每条回执都被判 failed，
   失败原因永远显示"探针未给出失败原因"，看起来像探针的问题。
3. **`appliedAt` 类型错误**：探针给的是 ISO-8601 字符串（`"2026-11-16T14:57:29Z"`），
   CP 声明成 `int64`，导致**整条回执反序列化失败被丢弃**。这个缺陷最具欺骗性：矩阵显示
   "探针没回执"，而探针其实早就应用成功了。现用 `json.RawMessage` 接住原始形态，
   ISO 字符串与 epoch 毫秒都认。

前两个修好后假探针测试即通过；第三个只有真机才暴露——假探针按 Go 结构体发时间戳，
天然绕过了它。这是"假探针 + 真机"两层验收互补的直接证据。

4. **连接态词表不匹配**：探针上行用 `online`（`BridgeConnectionState.ONLINE.wireName`），
   而 CP 合成值用 `connected`。`SynthesizeManagedConnection` 只认 `connected`，
   `online` 落进 fallback 分支——**在"在线"这一项上侥幸正确**，所以真机联调没暴露它。
   但这意味着白名单是残缺的：任何新取值都会被静默记成"在线"，把"不确定"说成"可用"。
   已在 `ManagedBridgeConnectionOnline` 显式收口，并用
   `TestSynthesizeManagedConnection`（9 子例）钉住四态映射。

### 可视化改造阶段暴露的另外三个（做界面时才显形）

5. **`IsForbiddenConfigPath` 前缀回溯死循环**：索引推进写成
   `idx = strings.IndexByte(key[idx+1:], '.') + idx + 1`，当剩余串已无 `.` 时
   `IndexByte` 返回 -1，而 `-1 + idx + 1 == idx`——索引原地踏步，循环永不退出。
   两段键（`bridge.enabled`）恰好能提前 break，故**只测两段键的用例完全看不见它**；
   真机表现为下发含 `alert.channels.webhook.url` 的配置时 CP 线程卡死。
   改用 `strings.Split` 逐段拼接，长度严格递减不可能不推进。

6. **缺少 `bridge` 整组结构性禁止**：探针侧有 `FORBIDDEN_ROOTS = setOf("bridge")`
   （整棵子树禁止），CP 侧只做了逐键禁止。于是 `bridge.line.max-inbound-bytes` 这类
   未登记的深层子键被判成"可远程写"——探针仍会拒，但 CP 预检就此失效，两边各说各话。

7. **`tier` 字段返回中文导致禁止键可编辑**：元数据接口把 `ConfigTier.String()`（中文"禁止"）
   直接塞进 JSON，前端按 `tier === 'forbidden'` 判断只读，条件**永远不成立**——
   用户在界面上改了 `bridge.*` 键，下发时才被拒，而界面从未提示它不可改。
   已拆出 `WireName()`（英文枚举，给机器）与 `String()`（中文，给人），
   并用 `TestManagedKeyCatalog_TierIsWireNameNotChinese` 钉住。

这三者与前面 4 个同源：**字段有定义、但没人真的用它做过判断**，所以错误不会显形。
5 与 7 尤其典型——它们都躲过了既有测试，只在"真的把值拿去比较 / 真的在界面上点"时才暴露。

### YAML 化阶段暴露的第四个（改编辑格式时才显形）

8. **catalog 与 layer 到达顺序把草稿锁成空**：草稿文本原先用
   `useState(初始值) + effect(catalog 到达后重建)` 维护。两个请求独立返回，
   若 catalog 先到而 layer 还没到（此时是空配置），effect 会用空配置重建一次并标记
   "已注入注释"，于是**草稿被永久锁成空串**，随后 layer 数据到达也不再更新——
   界面表现为「明明有配置，输入框却是空的」。
   改为「派生值 + 覆盖值」：未编辑时永远由 `(layer.config, catalog)` 现算，
   两者谁后到都得到正确结果；用户一旦编辑才切到他自己的文本。

9. **目录为空时序列化把所有键丢掉**：原实现拿「目录键集合」去筛"目录未覆盖项"，
   方向反了——catalog 为空时所有键都被判为已覆盖，输出空文本。
   这条与 8 叠加后果相同（都是空编辑器），故一并写死：序列化的键集合只由配置决定。

### 待定

- 回滚只支持"整批重推冻结快照"，不支持按子集细粒度回滚到任意历史版本。
- 矩阵连接维度的 `reportedConnection` 与 `connection` 并存：前者是探针自报（断连时会停在过期值），
  后者是合成值（桥断连即 `disconnected`）。展示口径需在页面上讲清楚，否则运维会困惑。
- 真机测试依赖外网下载 Paper 与探针 jar，CI 中默认 skip。
- 作用域切到 `instance` 后仍需手动选择目标；只有一台实例时是否自动选中，待产品决策。
