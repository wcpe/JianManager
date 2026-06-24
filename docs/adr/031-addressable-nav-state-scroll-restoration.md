# ADR-031: 导航与视图状态可寻址化 + 滚动位置恢复

- **日期**: 2026-06-24
- **状态**: accepted（骨架；随 FR-128 落地）
- **上下文**: 运维控制台当前把「在工作区打开的实例」存在 `console.ts` 的 `openInstanceId`（store 状态），并**刻意不进 URL**（`console.ts` 注释：「避免与既有 `/instances/:id` 详情路由语义冲突」），且 `Workspace` 监听到路由变化就强制 `closeInstance()`。后果：①打开实例不产生历史项，浏览器前进/后退与**鼠标侧键**无法在「列表 ↔ 已打开实例」间往返；②列表筛选/分组、详情激活 Tab、群组/节点的下钻（模态/行内 state）也都不进 URL/历史，无法深链、刷新即丢、后退无法「返回上一个位置」；③内容滚动发生在 `Workspace` 的内层 `overflow-auto` 容器（外层 `h-screen` 锁死 window 不滚），浏览器/路由的滚动恢复天然失效，且全应用未用 `<ScrollRestoration>`。用户诉求明确：**支持返回上一个位置 + 鼠标侧键上下历史**。

## 决策

**视图状态可寻址化（URL 化）+ 接入滚动位置恢复；修订 ADR-009/console.ts「打开实例不进 URL」的取舍。**

1. **打开实例走路由**：在工作区打开实例 = `navigate('/instances/:id')`，以 `/instances/:id` 为单一真相，移除 `openInstanceId` 双轨与「路由变即 closeInstance」hack。前进/后退/鼠标侧键、刷新、可分享链接自然成立。
2. **关键视图状态进 URL**：列表筛选/分组、详情激活 Tab、工作区打开的面板（ADR-030）、群组/节点的下钻，统一用 `useSearchParams`/子路由承载（如 `/instances?status=running&group=survival`、`?tab=files`），可深链、刷新还原、后退回到上一个状态。
3. **下钻进历史栈**：群组「查看」、节点详情展开等下钻从「模态/行内 state」改为可寻址（路由或 searchParams），使其产生历史项——鼠标侧键/后退能逐级收起下钻而非直接跳出整页。
4. **滚动位置恢复**：让内容滚动容器可被恢复——接入 `react-router` v7 `<ScrollRestoration>`，或对内层滚动容器自建恢复（按 `location.key` 存 sessionStorage、路由变化时还原）。

## 理由
- 直接兑现用户的「返回上一个位置 + 鼠标侧键前进后退」诉求，且是其唯一可行的技术基础（状态不进历史栈，侧键/后退无从恢复）。
- URL 即应用状态的单一真相，顺带获得深链、分享、刷新不丢、可被监控/审计引用。
- 原「不进 URL 避免与 /instances/:id 冲突」恰好反了——应让二者统一到同一路由，而非各存一份。

## 后果
- `console.ts` 移除 `openInstanceId`（或退化为派生自路由），`Workspace` 不再强制 closeInstance。
- 各列表页筛选/分组状态改由 `useSearchParams` 持有（受影响：实例/节点/群组/告警/日志等）。
- 与 ADR-030 协同：工作区打开的面板/资源进 URL，刷新/分享可复现工作区布局。
- 需统一一个「页面滚动容器」约定，供滚动恢复挂载。

## 关系
- **ADR-009（运维控制台为主 Shell）**：本 ADR 修订其「打开实例存 store、不进 URL」的实现取舍，shell 三段式布局不变。
- **ADR-030（可分屏面板工作区）**：面板树的可寻址由本 ADR 承载。
- **FR-128（导航与视图状态可寻址化 + 滚动恢复）**：本 ADR 的落地 FR。
