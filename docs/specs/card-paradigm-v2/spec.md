# 全站卡片范式提质 + 节点页卡片重设计（FR-246）

> 状态：开发中 · 增强 FR-163（视觉底座）/ FR-176（交互细节）/ FR-177（节点页）落地质量 · 关联 ADR-032

## 1. 需求（用户走查原话 → 代码根因）

「整个页面的卡片都很连接、圆角不符合、边角还有瑕疵、阴影太假」+ 节点页卡片空荡。代码根因：

1. **阴影太假**：`index.css` `--shadow-lift`（hover）= `0 12px 28px -6px brand/0.18 + 0 4px 10px -5px brand/0.12`——**大面积品牌色（靛蓝/青绿）晕染** + 28px 大模糊，hover 时像发光，显假。
2. **圆角不符**：卡片范式基准 `rounded-xl`(16px)，但全站散用 `rounded-lg`(12px) / `rounded-2xl`(20px) / `rounded-md`，不一致。
3. **很连接**：卡片网格 `gap-3`(12px) 偏小 + 弱边框，相邻卡片视觉粘连、缺呼吸。
4. **边角瑕疵**：`border` + `shadow-soft` + 圆角交界处的脏边（半透明边框叠柔阴影）。
5. **节点页卡片**：FR-177 已交付但信息密度不足、空荡。

## 2. 设计（卡片范式 v2）

### 2.1 阴影自然化（去「假」）
- `--shadow-lift` 去品牌色大晕染，改**中性、柔和、低抬升**：目标 `0 6px 16px -4px rgb(var(--shadow-color)/0.12), 0 2px 6px -3px rgb(var(--shadow-color)/0.08)`（中性 slate、模糊收敛、不发光）。
- `--shadow-soft`（静置）保留双层但微调更自然（边缘不糊）。
- hover 仍**只换阴影、不位移**（守 FR-176）。

### 2.2 圆角与边框统一
- 卡片统一基准 `rounded-xl`(16px=radius+4)；卡内嵌块 `rounded-lg`(12px) / 小控件 `rounded-md`，按层级递减。
- 审计全站散用 `rounded-2xl` 的卡片归一到 `rounded-xl`（除特例）。
- 边框统一 `border`(0.5px 语义) + 与阴影协调（去脏边）。

### 2.3 间距（去「连接感」）
- 卡片网格 `gap-3 → gap-4`(16px)；卡内 padding 统一 `p-4`/`p-5`（按卡密度）。

### 2.4 全站贯彻
- `Panel`（`components/ui/panel.tsx`）为**单一卡片范式**承载；审计各页（实例 / 节点 / 监控 / 运行时 / 分发 / 设置…）的卡片归一到 Panel 或同款 token（不自写 `rounded-* border bg-card shadow-*`）。
- `InstanceWorktableCard` 等运行实体卡沿用同 token。

### 2.5 节点页卡片重做（FR-177 提质）
- 节点卡信息密度：节点名 + 在线状态（呼吸灯）+ CPU/内存水位条 + 实例数 + 角色/维护标，不空荡；卡片范式与全站一致。

## 3. 验收

- [ ] 卡片 hover 阴影**自然**（非品牌色大晕染、不发光）；静置阴影柔和无脏边。
- [ ] 全站卡片圆角 / 边框 / 间距一致（审计无散用 `rounded-2xl`/杂 gap）；卡片间有呼吸、不粘连。
- [ ] 节点页卡片信息充实、不空荡。
- [ ] 前端 `tsc`/`eslint`/`vitest` 绿。
- [ ] **真机/浏览器走查**：双主题（靛蓝 / 青绿）× 明暗，卡片观感舒服、一致（视觉主观项，需用户确认通过）。

## 4. 关联

`web/src/index.css`（`--shadow-soft`/`--shadow-lift`/radius token）、`components/ui/panel.tsx`、`components/console/InstanceWorktableCard.tsx`、`pages/NodesPage.tsx`（节点卡）、各页卡片用法审计。

## 5. 备注

阴影/圆角/间距的**具体数值需视觉迭代**：实现时用真机浏览器双主题×明暗逐项对比、由用户拍板观感，spec 给方向与目标值、不锁死像素。
