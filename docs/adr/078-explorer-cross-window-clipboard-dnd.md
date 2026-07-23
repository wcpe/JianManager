# ADR-078: 资源管理器跨窗剪贴板与拖放总线

- **日期**: 2026-07-23
- **状态**: accepted
- **关联**: FR-377（依赖 FR-376 多窗壳）；粘贴语义仍遵循既有 `clipboard.ts`（cut=rename、copy=单文件 read+write）
- **取代关系**: 无（加性；不改 Worker/CP 文件 API）

## 上下文

FR-376 后同一实例可有多个 `ResourceExplorer` 实例（主区标签、浮动窗）以及独立浏览器标签（`/instances/:id/files`）。既有实现将剪贴板与拖拽源存在**组件 useState** 内，导致：

- 标签 A 剪切 → 标签 B / 浮动窗无法粘贴；
- 浮动窗 ↔ 主区树拖放无共享 `dragName`；
- 两个浏览器标签之间完全隔离。

系统文件拖入上传仍走 HTML5 `Files`，与本决策正交。

## 决策

1. **实例级剪贴板总线（模块单例）**  
   - Key：`instanceId`（跨标签仅同实例互通；不同实例互不读写）。  
   - 载荷：`{ mode: 'cut'|'copy', entries: ClipboardEntry[], updatedAt, sourceId }`，与 FR-070 `Clipboard` 同构。  
   - **同文档多 Explorer**：`subscribe` + 内存 map；set 时广播。  
   - **跨浏览器标签**：`BroadcastChannel('jm-explorer-clip-v1')` 同步 set/clear；辅以 `sessionStorage` 键 `jm.explorer.clip.${instanceId}` 供后开标签读取最近一次（刷新后可选恢复 cut/copy，**成功 cut 粘贴后必须 clear**）。  
   - **幽灵防护**：cut 成功粘贴后 clear；总线条目带 `updatedAt`；可选 TTL（默认 30 分钟，过期 get 视为 null）。关闭标签不强制 clear copy，但 cut 在任意成功完整粘贴后清空全通道。

2. **跨窗 DnD 载荷**  
   - 拖拽开始时写入 `dataTransfer`：自定义 type `application/x-jm-explorer-entries`（JSON：`{ instanceId, entries }`）+ `effectAllowed = 'move'`。  
   - 同页备用：总线 `setDragPayload`（内存，仅同文档），因部分浏览器自定义 MIME 在同页可靠、跨页受限。  
   - 放置方：优先读 `dataTransfer`；否则读总线 drag payload；`instanceId` 不一致则拒绝。  
   - 语义：资源树内放置 = **move**（与现有 `onDropMove` 一致）；不在本 FR 用 DnD 做 copy（copy 走剪贴板）。  
   - 系统文件 `Files` 类型仍优先走上传，不走条目 JSON。

3. **不引入**  
   - SharedWorker / Service Worker 消息中枢；  
   - 后端剪贴板 API；  
   - 跨实例剪贴板。

## 理由

- 前端纯壳能力，与三进程边界无关；BroadcastChannel 为浏览器标准且足够。  
- 复用 `planPaste` 保持 rename/copy 守卫（into-self、name-conflict 等）。  
- sessionStorage 镜像解决「后开标签收不到历史 BC 消息」的冷启动问题。

## 后果

- `ResourceExplorer` 剪贴板改为总线读写；多标签/浮动/深链页共享。  
- DnD 跨 Explorer 实例可用；跨浏览器标签 **粘贴** 必达，**拖放** 在浏览器允许自定义 MIME 时尽力，否则用户用剪切粘贴。  
- 新模块 `explorer-clipboard-bus.ts` + 单测；新 ADR 编号 078。

## 否决的替代

| 方案 | 否决原因 |
|---|---|
| 仅 lift state 到 TabHost | 无法覆盖浏览器新标签 |
| localStorage 轮询 | 噪声大、竞态差；BC 更合适 |
| 全局唯一 Explorer 单例 | 与 FR-376 多实例模型冲突 |
