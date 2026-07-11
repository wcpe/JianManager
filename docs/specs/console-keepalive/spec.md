# 功能规格：服务器控制台 keep-alive 缓存体系

> 状态：草拟　·　关联 PRD：FR-295 / FR-296（FR-297 数据层调优同分支实现，免 spec 仅在此登记边界）　·　分支：feature/fr-295-console-keepalive　·　关联 ADR：ADR-067（前端 keep-alive 与终端连接管理架构，预留号）

## 1. 背景与目标

服务器统一控制台（FR-269）用 `?tab=` 切 9 个页签，**切走即卸载重建**：终端 WS 断开重连、文件树/编辑器状态丢失；跨服务器切换时整个路由子树 remount（`Workspace.tsx` 按 pathname 换 key）。运营者「终端↔配置来回切」「多服之间来回巡检」是高频动作，每次都要付重建与重连成本。

目标：**页签访问过即保活（FR-295）+ 最近 3 个服务器控制台整体热缓存（FR-296）**，来回切换瞬时呈现、终端不断线不丢缓冲。React 已是 19.2.6，官方 `<Activity>` 可用。

## 2. 需求（要什么）

**FR-295 页签 keep-alive + 终端连接管理器：**
- 单服控制台内，访问过的页签切走不卸载、切回瞬时呈现（DOM 与本地状态保留）
- 终端页签往返：WS 不断开、滚动缓冲与输入历史不丢
- 文件/配置页签往返：目录展开态、打开的文件、未保存草稿保留
- 隐藏页签停掉自身轮询（不做无谓后台刷新）
- 离开该服控制台（被 FR-296 淘汰或关闭）时整体释放

**FR-296 跨服务器热缓存：**
- 最近打开的 ≤3 个服控制台整体保活（LRU），A↔B 切换双向瞬时
- 缓存中（后台）的服，终端 WS 保持连接、缓冲连续
- 后台闲置超过 10 分钟：断该服 WS 降级为界面状态缓存；再次进入自动重连
- 打开第 4 个服：LRU 淘汰最久未用者，完整卸载释放（WS 断、内存归还）

**范围外（不做）：**
- 控制台之外的普通页面（列表页等）不做组件级 keep-alive（YAGNI，用户已拍板）
- 未保存草稿的跨会话持久化（localStorage 草稿箱）不做
- 后端/Worker 侧不改动；WS 服务端协议不变

## 3. 设计（怎么做）

架构决策见 **ADR-067**（`<Activity>` keep-alive + 模块级终端连接管理器 + LRU 热集三件套），本节只写实现设计。

### 3.1 终端连接管理器 `terminal-session-manager.ts`（新，`web/src/lib/`）

模块级单例，按 `instanceId` 持有终端会话：

```
TerminalSession = {
  instanceId, term: xterm.Terminal,   // xterm 实例常驻管理器（内部 buffer 即滚动缓冲）
  ws: WebSocket | null, state: 'connected' | 'idle-disconnected' | 'closed',
  idleTimer, lastVisibleAt
}
```

- **组件订阅制**：`TerminalPane`/`Terminal` 组件不再自建 WS 与 xterm——mount 时 `manager.acquire(instanceId)` 拿会话、把 `term.open(containerEl)` 附着到自己的 div 并 `fit()`；unmount（含 Activity 隐藏）只 detach，不断 WS、不 dispose term。
- **生命周期**：`acquire` 首次创建会话并连 WS（token 复用现有 `useTerminalToken` 逻辑，改为管理器内可调用的 imperative 取 token 函数，语义不变）；`markHidden(instanceId)` 启动 10 分钟闲置计时；`markVisible` 取消计时、若 `idle-disconnected` 则自动重连（重新取 token）；`dispose(instanceId)` 断 WS + `term.dispose()`（仅 FR-296 淘汰/退出登录时调用）。
- 现有 `Terminal.tsx` 的重连/退避/只读逻辑迁入管理器，组件保留渲染与交互壳。
- 常量集中可调：`HOT_SET_SIZE = 3`、`IDLE_DISCONNECT_MS = 10 * 60_000`。

### 3.2 页签 keep-alive（FR-295，改 `InstanceConsolePage.tsx`）

- 页签容器改为：**访问过的页签进入 `mountedTabs` 集合**，全部渲染，非活跃者包在 `<Activity mode="hidden">` 中——DOM 与 state 保留、effects 卸载（TanStack Query 订阅随之暂停 → 隐藏页签自动停轮询，正合需求）。
- 终端 WS 不受 Activity 卸 effects 影响（连接在管理器手里）；页签重新可见时 effect 重跑 → re-attach + `fit()`（xterm 离屏期间容器尺寸为 0，重新可见必须 fit 重排）。
- overview 页签保持常驻轮询（活跃页签本来就可见）；其余页签隐藏即停刷。
- 未保存草稿守卫语义不变：草稿在页签切换间天然保留；离开整个控制台仍走既有守卫。

### 3.3 跨服热缓存宿主（FR-296，新 `InstanceConsoleCache.tsx` + 改 `Workspace.tsx`）

- `/instances/:id` 路由改由 **缓存宿主** 渲染：宿主维护 `hotSet: instanceId[]`（LRU ≤3），对每个成员渲染一份 `<Activity mode={active ? 'visible' : 'hidden'}>` 包裹的 `InstanceConsolePage`。
- 路由参数变化 → 命中热集直接切 visible（瞬时）；未命中 → push 进热集，超容淘汰 LRU 尾。
- **淘汰偏好**：优先淘汰无未保存草稿的实例；被迫淘汰带草稿实例时 toast 警示（防静默丢稿）。淘汰即 `manager.dispose` + 从热集移除（组件真卸载）。
- 实例转入 hidden → `manager.markHidden`；转回 visible → `manager.markVisible`。
- `Workspace.tsx` 的路由过渡 key：`/instances/:id` 统一归并为固定 key（如 `instances-console`），实例间切换不再触发路由级 remount/进场动画重放。
- 退出控制台外壳/登出时清空热集并 dispose 全部会话。

### 3.4 数据层调优（FR-297，改 `web/src/api/instances.ts` 等）

- 实例域查询（`useInstance`/`useInstanceMetrics`/`useServerState` 等）：`gcTime` 提至 15 分钟、`placeholderData: keepPreviousData`，回切先呈现缓存后台刷新；过渡态 2s 轮询逻辑不变。
- 侧栏常驻列（FR-293）与服务器选择器行 **hover 预取**：`prefetchQuery(['instances', id])`，150ms 防抖，仅预取详情不预取指标。

### 3.5 Mock 先行（用户硬性要求）

MSW 基座（ADR-047）已全仿真 WS 终端——三层能力全部先在 `VITE_MOCK` 模式下开发并可演示（页签往返不重连、跨服瞬切、淘汰行为），再接真栈真机。新增数据需求同步补 MSW handler（预计无需新增）。

## 4. 任务拆分

- [ ] FR-297：实例域查询 gcTime/placeholderData 调优 + hover 预取（先行独立 commit，vitest 全绿）
- [ ] FR-295：`terminal-session-manager` TDD（acquire/detach/markHidden/markVisible/dispose/闲置断连/重连取新 token 的状态机 vitest）
- [ ] FR-295：`Terminal.tsx`/`TerminalPane` 改造为订阅管理器（渲染壳 + attach/fit）
- [ ] FR-295：`InstanceConsolePage` 页签 Activity keep-alive（mountedTabs + 隐藏停轮询）
- [ ] FR-295：DOM 测试——页签往返 WS 不断（MSW WS mock 断言连接数不变）、草稿保留、隐藏页签停轮询
- [ ] FR-296：`InstanceConsoleCache` 宿主（LRU3 + 淘汰偏好 + toast）+ `Workspace.tsx` 路由 key 归并
- [ ] FR-296：DOM 测试——3 服循环切换组件不重建、第 4 服淘汰 LRU 尾并 dispose、闲置计时断连重连
- [ ] ADR-067 落盘（预留号，标题/文件名用 067）
- [ ] mock 模式演示走查（页签往返/跨服瞬切/淘汰）
- [ ] 文档同步：PRD 状态、ARCHITECTURE（前端架构节补 keep-alive 与连接管理）、CHANGELOG 末尾追加

## 5. 验收标准

- 终端↔文件/配置页签往返 ≥5 次：WS 连接数保持 1、滚动缓冲连续、编辑器草稿保留（DOM 测试 + mock 演示）
- 隐藏页签的查询轮询停止（测试断言观察者数/请求数不再增长）
- 3 个服 A→B→C→A 循环：组件实例不重建（状态保留断言）、终端各自缓冲连续；打开第 4 个服 D：LRU 尾被 dispose（WS close 被调、term dispose 被调）
- 后台服闲置超时（测试用假计时器）：WS 断、状态转 `idle-disconnected`；切回自动重连并取新 token
- 现有 vitest 全量绿；`tsc --noEmit`、eslint 无 error
- **真机验收（必过，测试绿不替代）**：真栈下终端来回切无重连痕迹（服务端无新握手日志）、3 服循环切换体感瞬时、闲置 10 分钟后切回自动重连成功——需用户确认通过

## 6. 风险 / 待定

- **Activity 语义**：`<Activity mode="hidden">` 卸载 effects——凡把资源挂在组件 effect 里的旧逻辑都要排查（本设计已把终端连接上收管理器；其余页签若有类似资源随排查处理）
- **xterm 离屏尺寸**：隐藏期间容器为 0 尺寸，重新可见必须 fit；分屏/窗口 resize 期间隐藏的终端在可见时补一次 fit
- **内存上限**：3 服 × 9 页签全保活的 DOM/内存占用需在 mock 演示时观察；若超预期可把重型页签（终端/资源）之外的保活策略降级（spec 内常量可调，不改架构）
- **WS 服务端资源**：最多 3 条终端 WS 常连 + 10 分钟闲置断连兜底，Worker 侧无改动
- 富交互测试环境（jsdom 无真实布局）下 xterm fit 断言需 mock ResizeObserver——沿用既有测试基座做法
