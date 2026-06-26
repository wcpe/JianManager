# 功能规格：FR-164 全局双主题（靛蓝/青绿）+ 明暗

> 状态：草拟（待审核）　·　关联 PRD：FR-164　·　依赖：FR-163（已落 master）　·　批 2 / worktree W1(frame)

## 1. 背景与目标
FR-163 已把品牌色全 CSS 变量化（`--primary`/`--accent`/`--ring`/`--brand-shadow`），并有 `.dark` 明暗。本 FR 在此底座上加**第二主题色（青绿 `#14B8A6`）** + **一处切换全站**的主题切换器，与明暗模式**正交**、`localStorage` 持久。P1。

## 2. 需求（要什么）
### 范围内
- 第二主题 **青绿 `#14B8A6`**：`[data-theme="teal"]` 覆盖品牌变量组（`--primary`/`--primary-foreground`/`--accent`/`--accent-foreground`/`--ring`/`--brand-shadow`/`--chart-1`），明暗各一套（`[data-theme="teal"]` 与 `[data-theme="teal"].dark`）。靛蓝为默认（无 data-theme 即靛蓝，承 FR-163）。
- **主题切换器**：侧栏底部（design §7），靛蓝/青绿圆点直选（复用 preview.html `.dotc` 观感）+ 明暗切换。一处切，全站 CSS 变量实时跟变（曲线/标签/按钮/进度条随主色）。
- **持久 + 正交**：主题色（indigo/teal）与明暗（light/dark/system）各自 `localStorage` 持久、互不干扰；刷新保持；首屏无闪烁（初始化时即套 `data-theme` + `.dark`）。
- 切换器在**所有已登录页**可达（侧栏常驻）；登录/初始化页保持默认靛蓝（无侧栏，沿用系统明暗）。

### 不做（范围外）
- 各业务页范式套用（其他 worktree）。
- 第三主题色 / 自定义取色。
- 主题切换器之外的侧栏导航重构（属 FR-131，同 W1 但单独提交）。

## 3. 设计（怎么做）
- `index.css`：在 `.dark` 之后加 `[data-theme="teal"]{...}` 与 `[data-theme="teal"].dark{...}`，只覆盖品牌变量组（结构色/状态色不动）。teal 取值：主色 `oklch(~0.7 0.12 182)`、accent 淡染、brand-shadow rgb（20 184 166）。
- 主题状态：扩展或新建 store 管 `colorTheme: 'indigo'|'teal'`，`applyColorTheme` 在 `<html>` 设 `data-theme`（indigo=移除属性）；与既有 `stores/theme.ts`（明暗）正交。初始化 `loadFromStorage` 同时套主题色 + 明暗，**并在登录前后都生效**（修 FR-163 自查发现的「明暗初始化仅在 console shell 跑」——本 FR 把主题/明暗初始化提到 app 入口 `main.tsx`/`App`，登录页也套）。
- 切换器组件 `ThemeSwitcher`（侧栏底部）：主色圆点（indigo/teal）+ 明暗按钮；纯函数 `lib/theme.ts`（`resolveColorTheme`/`nextMode` 等）下沉可测。
- 纯逻辑（持久键解析、属性套用决策）抽纯函数 + vitest（项目无 @testing-library，组件视觉靠真机）。

## 4. 任务拆分
- [ ] 测试先行：`lib/theme.ts` 纯函数（colorTheme 解析/持久/data-theme 决策）红→绿
- [ ] `index.css` 加 teal 品牌变量覆盖（明/暗）
- [ ] 主题色 store + `applyColorTheme`；明暗/主题初始化提到 app 入口（登录页也套）
- [ ] `ThemeSwitcher` 组件 + 接入侧栏底部
- [ ] 持久 + 首屏无闪 + 正交验证
- [ ] PRD FR-164 → 开发中；CHANGELOG 追加；doc-sync（ARCHITECTURE 主题机制）
- [ ] tsc/lint/vitest/build 全绿 + 真机（切换 + 刷新保持 + 明暗正交）

## 5. 验收标准
- 侧栏一处切靛蓝↔青绿，全站主色实时跟变（按钮/曲线/选中态/进度条），结构色/状态色不变。
- 主题色 × 明暗正交：四组合（靛蓝亮/靛蓝暗/青绿亮/青绿暗）均正常；刷新后保持；首屏无闪烁。
- i18n 中/英；登录/初始化页默认靛蓝正常。
- 自动化：纯函数 vitest 绿 + tsc/lint/build 绿。**真机由用户确认**（四组合 + 刷新 + 切换即时）。

## 6. 风险 / 待定
- 首屏无闪需在 `<head>`/入口尽早套 data-theme + .dark（早于 React 挂载），注意 SSR 无关（纯 CSR）。
- 与 W1 内 FR-131 侧栏重构协调：切换器是侧栏底部一块，同一 agent 顺序提交（先 FR-131 侧栏骨架，再 FR-164 切换器嵌入），避免自冲突。
