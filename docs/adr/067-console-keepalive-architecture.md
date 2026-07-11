# ADR-067: 前端控制台 keep-alive 与终端连接管理架构

- **日期**: 2026-07-12
- **状态**: accepted
- **取代关系**: 无（纯前端渲染/连接生命周期决策，不触碰进程边界与通信协议；与 ADR-035 导播台预热并发模型并存——导播台继续以 `content-visibility` + 渲染暂停管理多场景，本 ADR 管服务器统一控制台的页签与跨服缓存）。
- **关联**: FR-295（页签 keep-alive + 终端连接管理器）、FR-296（跨服控制台 LRU 热缓存）、FR-297（实例域数据层调优）、FR-269（服务器统一控制台）、FR-140（一次性终端 token 重取）、ADR-061（WS 令牌密钥）；spec 见 `docs/specs/console-keepalive/spec.md`。

## 上下文

服务器统一控制台（FR-269）用 `?tab=` 切 9 个页签，切走即卸载重建：终端 WS 断开重连、文件树/编辑器状态丢失；跨服务器切换时整个路由子树按 pathname 换 key remount。运营者「终端↔配置来回切」「多服来回巡检」是高频动作，每次都付重建与重连成本。React 已是 19.2（官方 `<Activity>` 可用）。

约束：终端一次性 token 首连即被 CP 消费（FR-140），重连必须现取新 token；WS 服务端协议与 Worker 侧不改动；jsdom 测试基座（ADR-047）需可覆盖状态机与 DOM 行为。

## 决策

三件套（详细设计见 spec §3）：

### 1. 终端连接抽为模块级单例管理器（组件订阅制）

新增 `web/src/lib/terminal-session-manager.ts`：按 `instanceId` 持有 `{ xterm 实例, WebSocket, 状态机, 闲置计时 }`。xterm 实例与 WS 的生命周期**上收到管理器**，`Terminal.tsx`/`TerminalPane.tsx` 退化为渲染与交互壳——mount 时 `acquire` + `attach`（把常驻 xterm 附着到自己的容器并 fit），unmount（含 `<Activity>` 隐藏）只 `detach`，不断 WS、不 dispose xterm（其内部 buffer 即滚动缓冲，天然跨挂载保留）。重连/退避/一次性 token 现取逻辑随连接一并迁入管理器。

状态机：`connecting → connected →（闲置超时）idle-disconnected →（回切）connecting`；`dispose` 仅在 LRU 淘汰 / 显式释放 / 登出时调用（断 WS + `term.dispose()`）。

### 2. 页签与跨服缓存用 React `<Activity mode="hidden">` 保活

- **页签级（FR-295）**：`InstanceConsolePage` 维护 `mountedTabs`（访问过即入集合），非活跃页签包 `<Activity mode="hidden">`——DOM 与本地状态保留、effects 卸载，TanStack Query 订阅随之暂停 → 隐藏页签自动停轮询；重新可见时 effect 重跑 → 终端 re-attach + fit。
- **跨服级（FR-296）**：`/instances/:id` 由缓存宿主 `InstanceConsoleCache` 渲染，维护 `hotSet`（LRU ≤3，`HOT_SET_SIZE` 常量集中可调），每个成员一份 `<Activity>` 包裹的 `InstanceConsolePage`；`Workspace.tsx` 把 `/instances/:id` 的路由过渡 key 归并为固定值，实例间切换不再路由级 remount。
- **淘汰偏好**：优先淘汰无未保存草稿者（草稿脏态经模块级 `console-draft-registry` 上报，资源管理器 dirty 变化时登记）；被迫淘汰带草稿实例时 toast 警示。淘汰即 `manager.dispose` + 组件真卸载。

### 3. 闲置断连兜底（资源上限）

后台（不可见）实例闲置超过 `IDLE_DISCONNECT_MS`（10 分钟）：管理器断该服 WS、状态转 `idle-disconnected`（xterm 缓冲保留为界面状态缓存）；再次可见自动重连并**现取新一次性 token**（FR-140 语义不变）。上限画像：最多 3 条终端 WS 常连 + 10 分钟闲置断连，Worker/CP 侧零改动。

### 4. 数据层配合（FR-297）

实例域查询（详情/指标/服务器状态）`gcTime` 提至 15 分钟 + `placeholderData: keepPreviousData`，回切先呈现缓存后台刷新；服务器选择器（及后续侧栏常驻列）行悬停 150ms 防抖预取实例详情。

## 否决的替代方案

- **CSS `display:none` 手工保活**（不卸载 effects）：隐藏页签的轮询/动画继续空转，需逐组件手工停表；`<Activity>` 卸 effects 的语义恰好让 Query 订阅自动暂停，选官方原语。
- **把终端 WS 留在组件 effect、以「不卸载」换保活**：与 Activity 卸 effects 语义冲突，且跨服淘汰/登出时无统一释放点；连接上收单例管理器后生命周期单点可控、状态机可单测。
- **全站页面级 keep-alive**：列表页等重建成本低，YAGNI（用户拍板范围仅控制台）。

## 后果

- 终端来回切换不再产生服务端新握手；滚动缓冲/输入历史/编辑器草稿跨页签与跨服保留；3 服循环巡检瞬时呈现。
- **会话语义变化**：终端会话与组件解耦后按 `instanceId` 全局唯一——同一实例的终端在多个表面（控制台 + 超级工作台/导播台）同时打开时共享同一会话，xterm DOM 附着「后挂载者赢」；非控制台表面卸载时按既有语义释放（不在热集内即 dispose），导播台 ADR-035 的「cold 场景不建 WS」资源模型不变。
- 隐藏保活的 DOM/内存占用上限 = 3 服 × 已访问页签；超预期时可调 `HOT_SET_SIZE` 或降级重型页签保活策略（spec §6，常量可调不改架构）。
- 登出/离开控制台外壳时 `disposeAll` 统一释放，杜绝孤儿连接；jsdom 测试基座在每例后 `disposeAll` 保证用例隔离。
- 真机验收维度（服务端无新握手日志、3 服循环体感、闲置 10 分钟重连）不可被 jsdom 测试替代，发版前必须真机走查。
