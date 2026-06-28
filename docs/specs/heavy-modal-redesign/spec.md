# 功能规格：重度模态重做（创建实例 + 添加节点）

> 状态：待审　·　关联 PRD：FR-189（增强 FR-009 创建实例 / FR-004 添加节点对话框）　·　关联 ADR：无（前端重做，后端端点不变）　·　分支：feature/fr-189-heavy-modal-redesign

## 1. 背景与目标

真机走查暴露两个最重的模态「太难用 / 溢出」：
- **创建实例**（`CreateInstanceDialog.tsx:154`）：`max-w-md` 单列塞 ~10 个字段（名称/节点/类型/模板/启动方式/启动命令/工作目录/JDK/用户组…），虽有 `max-h-[88vh]` 可滚但又窄又长，且 Docker 资源限额字段在 direct/daemon 模式下无意义仍占位。
- **添加节点**（`AddNodeDialog.tsx:143`）：`max-w-2xl` **没套 `scrollable-dialog` 壳**，签发凭据后渲染长接入指引 → 顶出视口（截图实证）；且只有「自动安装(脚本)」一条路径，缺「手动连接(已自行部署的 worker 直接凭据上线)」。

这两个是 FR-188 全站审计里**最该深做**的两个，单列为 FR-189 重做（FR-188 不碰这两个文件，避免双改冲突）。采**方案 2**（分区/Tab 重排）。P1。**纯前端重做，后端 create/enroll 端点不变。**

## 2. 需求（要什么）

### 范围内
- **统一自适应壳**：两个对话框都套 `scrollableDialogContentClass` + `ScrollableDialogBody`（FR-072），头/脚固定、正文超高内部滚动，修 AddNode 溢出。遵循 `.claude/rules/ui-modals.md`。
- **创建实例分区/Tab 重做**：
  - 加宽（`sm:max-w-2xl`）+ 字段按「基本 / 启动 / 高级」分区（或 Tab），双列网格缩短高度。
  - **条件显隐**：Docker 资源限额相关字段仅在启动方式=Docker 时出现（direct/daemon 不显，去掉「不支持 cgroup」噪音占位）。
  - 字段语义/校验/提交 payload 不变（复用现 create mutation 与后端端点）。
- **添加节点 Tab 重做**：
  - 加「**自动安装**」「**手动连接**」两 Tab。
  - 自动安装 = 现有 enroll：签发一次性凭据 + 一键脚本（Linux/Windows）+ 手动分步兜底（沿用现内容，收进自适应壳内部滚动）。
  - 手动连接 = 面向「worker 二进制已自行部署/已拿到」的场景：展示 control-plane 地址 + 一次性 token + 让已部署 worker 连入的步骤（配置 `control-plane` 地址与 token、启动 worker 即注册），不走安装脚本。
  - 复制按钮改用共享 `copyToClipboard`（修 HTTP 非安全上下文复制失败）。
- i18n zh/en（只追加自己的键块）；暗亮主题用 token。

### 不做（范围外）
- 改后端 create-instance / enroll / issue-token 端点或数据模型。
- 客户端分发模态（FR-187）、其余 dialog 审计（FR-188）。
- Worker 二进制来源/下发（FR-190）——手动连接 Tab 只引导「已部署的 worker 连入」，二进制怎么来归 FR-190。

## 3. 设计（怎么做）

### 3.1 创建实例（`CreateInstanceDialog.tsx`）
- `DialogContent` 套 `scrollableDialogContentClass` + 正文进 `ScrollableDialogBody`；宽度 `sm:max-w-2xl`。
- 字段重组为分区（分区标题 + 双列 grid）或 shadcn `Tabs`：基本（名称/节点/类型/模板/用户组）、启动（启动方式/启动命令/JDK/工作目录）、高级（Docker 资源限额——仅 Docker 模式显）。
- Docker 字段用启动方式状态条件渲染。
- 提交 payload 与校验保持（仅布局重排）。

### 3.2 添加节点（`AddNodeDialog.tsx`）
- `DialogContent` 套自适应壳；用 shadcn `Tabs`（自动安装 / 手动连接）。
- 自动安装 Tab：现有签发 + 脚本 + 分步内容迁入。
- 手动连接 Tab：展示 CP 地址 + token + 「在已部署 worker 上配置并启动」步骤（复用 enroll token，同一签发结果两 Tab 共用）。
- 复制点改 `copyToClipboard`（共享 util，见预对齐）。

### 3.3 共享复制工具（预对齐，非本 FR 独有）
- `web/src/lib/clipboard.ts` 的 `copyToClipboard(text): Promise<boolean>`：优先 `navigator.clipboard.writeText`，不可用/抛错时回退 `document.execCommand('copy')` + 离屏 textarea（HTTP 非安全上下文可用）。本 FR 在 AddNode 调用点接入。

## 4. 任务拆分
- [ ] 创建实例：套自适应壳 + 加宽 + 基本/启动/高级 分区或 Tab + Docker 字段条件显隐
- [ ] 添加节点：套自适应壳修溢出 + 自动安装/手动连接 Tab + 手动连接步骤
- [ ] AddNode 复制点接入共享 `copyToClipboard`
- [ ] i18n zh/en 追加（实例分区标题 / 节点两 Tab 文案）；暗亮主题校验
- [ ] doc-sync：PRD FR-189「计划」→「开发中」（只改本行）；ARCHITECTURE 前端页面/对话框章节（如需）；CHANGELOG `[Unreleased]` 末尾追加一行
- [ ] 中文 commit（`refactor(web)`/`feat(web)` 拆 commit：壳合规=refactor，分区/Tab/手动连接=feat）

## 5. 验收标准
- 前端 tsc/eslint/build 绿；后端无改动。
- 创建实例：内容自适应不溢出；字段分「基本/启动/高级」分区或 Tab；Docker 资源限额字段仅 Docker 模式出现；创建提交行为不变。
- 添加节点：长接入指引在壳内**内部滚动、不再顶出视口**；有「自动安装/手动连接」两 Tab，手动连接展示 CP 地址+token+连入步骤；复制按钮在 HTTP（非安全上下文）下可用。
- 全程无「点击新增→内联展开表单」、无固定尺寸溢出。
- **【需真机，用户确认】** 浏览器在 `http://<LAN-IP>:8080`（非安全上下文）下：创建实例对话框分区/不溢出；添加节点对话框两 Tab 可用、长指引内部滚动、**复制按钮成功复制**；zh/en + 暗亮主题正常。

## 6. 风险 / 待定
- **与 FR-188 边界**：FR-188 审计**不碰** `CreateInstanceDialog.tsx`/`AddNodeDialog.tsx`（归本 FR）；本 FR 不碰其余 dialog。共享 `copyToClipboard` 由预对齐落 main，两 FR 各改自己调用点。
- **与 FR-190 协作**：手动连接 Tab 的「worker 已部署」前提，其二进制取得（CP 下发）归 FR-190；本 FR 只做 UI 引导，不依赖 FR-190 先落（token 连入逻辑现已存在）。
- **创建实例字段真相**：分区归类以现有字段为准，勿改字段语义/校验；Docker 条件显隐需对齐现「启动方式」状态。
