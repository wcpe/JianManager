# ADR-032: 前端设计系统底座（靛蓝 token + 统一卡片原语 + 双主题留口）

- **日期**: 2026-06-26
- **状态**: accepted（随 FR-163 落地）
- **上下文**: 前端整体重设计（FR-163~169）启动，设计探索定稿于 `docs/specs/ui-redesign/design.md`（+ 原型 `preview.html`）。现状问题：①主色为早期 MC 绿（`index.css` OKLCH hue 145），与定稿的「靛蓝圆角灵动」视觉语言不符；②卡片用法不统一——既有 shadcn `Card`（松散、各页自定 padding/圆角）又有 FR-061 起的 `Panel`，KPI 卡在 `OverviewPage` 内联手搓（局部 `Stat`），缺统一原语；③无柔和阴影/抬升/缓动/呼吸灯等「灵动」基础 token；④后续 FR-164 要做「靛蓝/青绿」双主题 + 明暗正交，若品牌色散落在组件里硬编码则无法一处换肤。需要先落一套**设计底座**，作为 FR-164~169 与各页范式套用的共同地基。

## 决策

**建立 token 驱动的前端设计系统底座：靛蓝主色重基 + 统一卡片原语（`Panel`/`StatCard`）弃 shadcn `Card` 松散用法 + 品牌色全变量化为双主题留口。**

1. **靛蓝主色重基**：`index.css` 默认主色由 MC 绿改**靛蓝 `#6366F1`**（OKLCH hue ~277），含明/暗两套；背景改中性冷灰、文字深石板灰（不纯黑纯白不死灰）。
2. **设计底座 token**：新增柔和弱阴影 `shadow-soft`、主色晕染抬升 `shadow-lift`（hover）、iOS 缓动 `ease-ios`（`cubic-bezier(.16,1,.3,1)`）、呼吸灯 `animate-breathing`（运行对象脉动光环）、大圆角基线（`--radius` 0.75rem，卡片 `rounded-xl`）。
3. **统一卡片原语**：`Panel`（分区/容器，新增可选 `icon`/`tone`/`hoverable`）+ 新增 `StatCard`（KPI 卡）为唯一卡片原语；「按指标混搭」等逻辑下沉纯函数（`lib/stat-card.ts`/`lib/tone.ts`）便于单测。**弃 shadcn `Card` 松散用法**：`card.tsx` 标 `@deprecated`，eslint `no-restricted-imports` 阻断业务代码新引入（定义文件保留以兼容历史）。
4. **双主题底座**：组件层**零硬编码品牌色**，品牌色全部经 CSS 变量（`--primary`/`--accent`/`--ring`/`--brand-shadow`）。FR-164 第二主题（青绿）只需新增 `[data-theme="teal"]` 覆盖这组变量即可全站换肤，与明暗模式正交。

## 理由
- token 驱动 + 变量化品牌色是双主题（FR-164）唯一可行的技术基础：散落硬编码无法一处切换。
- 单一卡片原语消除「Card vs Panel vs 手搓」的三重不一致，后续各页范式套用（批 2）有统一地基。
- 逻辑下沉纯函数契合项目既有测试范式（无 `@testing-library`，以 `.ts` 纯函数 + vitest 覆盖）。
- 靛蓝重基属定稿决策（design.md §1.2 + 用户确认），底座即终态观感，避免「先绿后换」返工。

## 后果
- 全站 `--radius` 增大、卡片普获柔和阴影/大圆角——视觉随之变化（预期，属底座升级）。`Panel` 新能力全 opt-in，既有 52 处用法零回归。
- 新增前端 FR 一律用 `Panel`/`StatCard`，不得新引入 shadcn `Card`（eslint 守门）。
- FR-164 仅覆盖品牌变量即可换肤；各页工作台卡/配置行范式（批 2）在此原语上套用。
- 横切验收（前端类）：i18n 中/英 + 明暗 + 真机三者缺一不算交付（PRD §6）。

## 关系
- **FR-163（视觉底座与设计系统）**：本 ADR 的落地 FR，spec 见 `docs/specs/ui-redesign/fr-163-visual-foundation.md`。
- **FR-061（高密度设计系统）**：本 ADR 在其 OKLCH token + 状态色系 + `Panel`/`MiniBar` 基础上扩展，不取代。
- **FR-164（全局双主题）**：消费本 ADR 的品牌变量留口；第二主题取值 + 切换器 UI 属 FR-164。
- **ADR-009（运维控制台 Shell）**：外壳布局不变，本 ADR 只动视觉 token 与卡片原语。
