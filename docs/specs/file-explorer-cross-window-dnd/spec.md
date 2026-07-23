# 功能规格：跨窗拖放 + 统一剪贴板总线

> 状态：开发中　·　关联 PRD：FR-377　·　分支：feature/fr-377-explorer-cross-window-dnd  
> 依赖：FR-376（多标签/浮动/深链页）  
> ADR：ADR-078  
> 计划：`.tmp/brainstorm-file-explorer-perm-ux-2026-07-23.md`

## 1. 背景与目标

多窗壳落地后，剪贴板与拖拽源仍是各 `ResourceExplorer` 本地 state，主区↔浮动↔浏览器标签无法互通。目标：

1. **统一剪贴板总线**：同实例任意 Explorer 剪切/复制/粘贴一致；跨浏览器标签同步。  
2. **跨窗 DnD**：主区与浮动窗之间拖放移动；跨标签以粘贴为主、DnD 尽力。  
3. 语义对齐既有 `planPaste` / rename / 单文件 copy；系统文件拖入上传不变。

## 2. 需求

### 范围内
- 实例级 clipboard bus（内存 + BroadcastChannel + sessionStorage 镜像）  
- ResourceExplorer 改接总线（去掉仅本地 clipboard state 作为真源）  
- DnD：`application/x-jm-explorer-entries` + 同页 drag payload  
- cut 粘贴成功 clear；TTL 防幽灵  
- 单测 bus；DOM：两 TabHost 签或双 Explorer 粘贴互通（同页）

### 不做
- 跨实例剪贴板；目录 copy；SharedWorker  
- 统一壳全场景（FR-378）

## 3. 设计

见 ADR-078。模块：`explorer-clipboard-bus.ts`。

## 4. 任务

- [x] ADR-078  
- [x] `explorer-clipboard-bus.ts` + 单测（6）  
- [x] ResourceExplorer 接总线 + DnD dataTransfer  
- [x] DOM 测：双 Explorer 剪贴互通 + ResourceExplorer/TabHost 回归  
- [x] CHANGELOG / PRD  
- [ ] 真机：双浏览器标签 copy→paste（用户确认）

## 5. 验收

1. 同页两 Explorer（或两标签）：A 剪切 → B 粘贴成功。  
2. 同页浮动 ↔ 主区拖放移动（或等价粘贴）。  
3. 两浏览器标签：A copy → B paste（BroadcastChannel / sessionStorage）。  
4. cut 完整粘贴后总线清空；过期条目不误粘贴。  
5. 系统文件拖入上传不回归。  
6. 自动化单测 + 关键 DOM 绿。
