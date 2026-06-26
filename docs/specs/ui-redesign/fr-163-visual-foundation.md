# 功能规格：FR-163 视觉底座与设计系统

> 状态：已验收（用户浏览器自查通过，待发版标交付）　·　关联 PRD：FR-163　·　分支：master（批 1，单跑 sdd-develop-feature）
> 设计依据：`docs/specs/ui-redesign/design.md` §1/§2 + 原型 `preview.html`

## 1. 背景与目标

前端重设计（FR-163~169）的**地基**。所有后续页面（FR-164 双主题、FR-165 分组、FR-166~168 工作区、FR-169 监控、以及批 2「增强批」的各页范式套用）都依赖一套统一的**设计 token + 卡片原语**。本 FR 只落地这套底座，**不重画任何业务页面**。

属于 P1。先落 main，解锁批 2 大并行。

## 2. 需求（要什么）

### 范围内
1. **设计 token 体系**（`web/src/index.css`）落地「靛蓝圆角灵动」视觉语言：
   - 主色基线切换为**靛蓝 `#6366F1`**（替换当前 MC 绿），含暗色变体。
   - 大圆角基线（卡片 ~16px）、**柔和弱阴影** token（常态 `--shadow-soft` + hover 抬升 `--shadow-lift`，后者带主色晕染）。
   - **iOS 缓动** token `--ease-ios: cubic-bezier(.16,1,.3,1)`。
   - **呼吸灯**动画 token + 工具类（运行对象脉动光环）。
   - 语义状态色（绿/琥珀/红/蓝）对齐设计取值，复用既有 `--status-*`（FR-061 已建）。
2. **统一卡片原语**：
   - 增强既有 `Panel`：柔和阴影 + 大圆角 + 可选 hover 抬升 + 可选图标/语义头；**向后兼容**现有 52 处用法（默认外观平滑过渡，不破坏布局）。
   - 新增 `StatCard`（KPI 卡）：图标块 + 标签 + 大数值 + 可选副值/趋势/`MiniBar`，右侧「按指标混搭」由**纯函数**决定（可测）。
3. **弃 shadcn `Card` 松散用法**：把仅有的 3 处（`LoginPage`/`SetupPage`/`SettingsPage`）迁移到 `Panel`；`card.tsx` 标注 `@deprecated`，加 eslint 约束阻止新引入。
4. **双主题底座（仅底座，不含第二主题）**：组件层**零硬编码品牌色**，品牌色全部经 CSS 变量（`--primary`/`--accent`/`--ring`/`--shadow-lift`）。使 FR-164 仅需新增 `[data-theme="teal"]` 覆盖这组变量即可全站换肤。
5. **响应式基线**：原语（`Panel`/`StatCard`）流式宽度、不破栅格；约定断点（Tailwind 默认 sm/md/lg）写入 ARCHITECTURE。
6. **canonical 用法证明**：用新 `StatCard` 替换 `OverviewPage` 内联的局部 `Stat`（等价替换，不改页面结构/数据）。

### 不做（范围外，留给后续批）
- 第二主题（青绿）取值 + 主题切换 UI + `data-theme` 属性切换逻辑 → **FR-164**。
- 各业务页面套用工作台卡 / 配置行 / 监控骨架范式 → **批 2 增强批**。
- 5 域导航 IA、侧栏折叠、面包屑外壳 → FR-112/FR-131（另批）。
- 多级分组、工作区画布、监控页升级 → FR-165~169。
- 新增任何后端/接口/数据模型。

## 3. 设计（怎么做）

### 3.1 token（`web/src/index.css`）
- `:root` 与 `.dark` 内的 `--primary`/`--primary-foreground`/`--ring` 由绿（hue 145）改为靛蓝（hue ~277，`#6366F1`）。
- `--accent`/`--accent-foreground` 重定为**主色淡染**（对应原型 `--pb`：选中态/图标块/头像/主色标签背景）。
- `--background` 由偏绿浅灰改为**中性冷灰**（`#F4F5F7` 量级）；`--muted` 对应原型 `--soft`。
- `@theme` 内新增：`--shadow-soft`、`--shadow-lift`（主色晕染）、`--ease-ios`、呼吸灯 `--animate-breathing` + `@keyframes`；`--radius` 由 `0.5rem` 提到 `0.75rem`（卡片走 `rounded-xl`≈16px）。
- 暗色同步给出对应值；阴影在暗色降为低不透明黑。

### 3.2 组件
- `Panel`（增强，保持 props 兼容）：默认 `rounded-xl` + `shadow-soft` + `border`（弱化）；新增可选 `hoverable`（`shadow-lift` + 轻抬，走 `--ease-ios`）、`icon`、`tone`（语义头底色）。**不改**既有 `title`/`actions`/`bodyClassName` 行为与默认密度。
- `StatCard`（新）：`web/src/components/ui/stat-card.tsx`。props：`label`、`value`、`sub?`、`icon?`、`tone?`（语义色块）、`level?`、可选 `bar`/`trend`/`delta`。右侧视觉选择逻辑抽到纯函数 `web/src/lib/stat-card.ts`（`pickStatVisual(kind)` 等）便于单测。
- `card.tsx` 加 `@deprecated` JSDoc；eslint `no-restricted-imports` 阻止 `@/components/ui/card` 新引入（迁移完 3 处后置为 error）。

### 3.3 ADR
- 新增 **ADR-032：前端设计系统底座**——记录：① 采用 token 驱动设计系统；② `Panel`/`StatCard` 为唯一卡片原语，弃 shadcn `Card` 松散用法；③ 默认品牌色由 MC 绿改靛蓝、品牌色全部 CSS 变量化为双主题留口。引用 design.md，不重复其正文。

### 3.4 文档
- `ARCHITECTURE.md` 前端架构章节补：设计 token 体系、卡片原语清单、响应式断点约定。
- `CHANGELOG.md` 未发布段记本次。

## 4. 任务拆分
- [x] 写 `pickStatVisual`/`deltaTone` 纯函数测试（先失败）→ 实现 `web/src/lib/stat-card.ts`
- [x] `index.css`：靛蓝 token 重基 + 新增 shadow/ease/breathing/radius token（含 `.dark`）
- [x] 增强 `Panel`（hoverable/icon/tone，向后兼容）
- [x] 新增 `StatCard` 组件 + `lib/tone.ts`（含单测）
- [x] `OverviewPage` 局部 `Stat` → `StatCard`（等价替换）
- [x] 迁移 `LoginPage`/`SetupPage`/`SettingsPage` 的 `Card` → `Panel`
- [x] `card.tsx` 标 `@deprecated` + eslint `no-restricted-imports` 阻断新引入
- [x] 呼吸灯工具类接入 `InstanceStatusDot`（运行态脉动）
- [x] 写 ADR-032；同步 ARCHITECTURE / CHANGELOG / PRD 状态
- [x] `tsc -b` + `eslint` + `vitest run`（291）全绿；构建产物核验新 token 工具类已生成
- [x] **真机走查（用户自查通过）**：浏览器自查登录（明/暗，按钮 `oklch(0.585 0.222 277)`、Panel `shadow-soft` 经 inspect 实测）+ 初始化 + 总览（StatCard `reactComponent` 实测）+ 设置（Card→Panel 实测）；indigo 控制台外壳、StatCard、双主题底座均渲染正常

## 5. 验收标准
- **测试**：`web` 下 `npm run test`、`tsc -b`、`npm run lint` 全绿；`pickStatVisual` 等纯函数有表驱动单测覆盖。
- **原语就位**：`Panel` 增强后既有 52 处用法外观不退化（构建通过 + 真机抽查代表页无错位）；`StatCard` 在 Overview 正常渲染（图标/数值/副值/条）。
- **弃 Card**：全仓 `@/components/ui/card` 业务引入归零（仅保留被标弃的定义文件）；eslint 对新引入报错。
- **双主题底座**：组件层 grep 无硬编码品牌色（无 `#6366F1`/裸 indigo 类）；手动把 `--primary` 改一个值即可全站换色（验证留口有效）。
- **横切验收（前端类，PRD §6）**：i18n 中/英完整、暗/亮色均正常、关键路径（登录→总览→设置）真机走查无异常——**三者由用户确认通过**，缺一不算交付。
- **响应式**：Overview KPI 行在窄/宽视口栅格自适应不破版（真机/缩放抽查）。

## 6. 风险 / 待定
- **关键边界（待用户在本 spec 审核时拍板）**：
  1. **靛蓝立即上 main**：批 1 即把默认主色由绿翻靛蓝（main 视觉立刻变靛蓝），青绿+切换器留 FR-164。推荐如此（FR-163 本就是「靛蓝基线」）。
  2. **只做底座、不重画页面**：除 Overview 的等价 `Stat→StatCard` 与 3 处 Card 迁移外，不动其他业务页结构。推荐如此（页面范式套用属批 2）。
- `Panel` 增强需保证 52 处零回归：默认外观尽量贴近现状（仅圆角/阴影微调），新能力全部 opt-in。
- 无 `@testing-library/react`：纯视觉部分以构建 + 真机为准，逻辑下沉纯函数补测（遵循项目既有测试范式）。
