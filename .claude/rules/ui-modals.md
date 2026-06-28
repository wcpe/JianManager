# 模态框纪律

> 适用于 `web/` 前端所有「创建 / 编辑 / 配置」类交互。违反即拒绝合并。

## 原则

「新增 / 编辑 / 配置」类的表单交互，**一律用模态框（Dialog）承载**，且模态框**内容自适应**。
禁止在页面内联展开表单、把原有布局顶开重排。

## 禁止：内联展开反模式

点击「新增 / 编辑」后，在**当前页面就地**渲染出一段表单、挤占或顶开原有内容（布局重排）。

```tsx
// ❌ 反模式：点击切 show，就地内联渲染表单，布局被顶开
function CreateChannelForm() {
  const [show, setShow] = useState(false)
  if (!show) return <button onClick={() => setShow(true)}>新增频道</button>
  return <form className="border rounded-lg p-4 ...">{/* 内联表单挤占页面 */}</form>
}
```

这种方式的问题：① 布局抖动、上下文丢失；② 表单与列表争夺垂直空间；③ 无统一的视口约束，长表单把页面撑长。

## 必须：内容自适应模态框

用 shadcn `<Dialog>` 承载，并套 `FR-072` 的高度自适应壳
（`web/src/components/ui/scrollable-dialog.tsx`）：

```tsx
// ✅ 正确：模态框 + 内容自适应（头/脚固定、正文超高内部滚动）
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'

<Dialog open={open} onOpenChange={setOpen}>
  <DialogContent className={scrollableDialogContentClass}>
    <DialogHeader><DialogTitle>新增频道</DialogTitle></DialogHeader>
    <ScrollableDialogBody className="space-y-3">{/* 表单字段 */}</ScrollableDialogBody>
    <DialogFooter>{/* 取消 / 创建 */}</DialogFooter>
  </DialogContent>
</Dialog>
```

约束（缺一不可）：

1. **宽度按内容**：用 `sm:max-w-*`（或默认 `sm:max-w-lg`）按表单繁简选，不写死像素宽。
2. **高度自适应 + 超高内部滚动**：长表单套 `scrollableDialogContentClass`（`max-h-[calc(100dvh-4rem)]`）+ `ScrollableDialogBody`；**禁止固定高度**、禁止内容超出视口被裁切或顶满屏。
3. **头/脚不滚、仅正文滚**：标题与操作按钮始终可见。
4. **裸 `fixed inset-0` 自绘模态**：复用 `MODAL_OVERLAY` / `MODAL_PANEL` 常量（同样受 `max-h-[88vh] + overflow-y-auto`），不得自写固定尺寸面板。

## 例外

- **危险确认**：删除/回滚等二次确认用 `DangerConfirm`（本身即模态，已合规）。
- **一次性只读展示**（如密钥明文一次性弹窗）：用 `Dialog` 展示即可，无表单也属模态。
- **行内编辑**（表格单元格就地编辑、开关切换等微交互）：不属「表单弹出」，不受本规则约束。
- **抽屉（Sheet/Drawer）**：分步向导等复杂表单可用抽屉替代模态，但同样要求内容自适应 + 内部滚动，禁固定尺寸溢出。

## 检查

- 新增「创建 / 编辑 / 配置」交互时，按本规则用模态/抽屉承载，套 `scrollable-dialog` 壳。
- Code Review / `/sdd-review` 检查 diff 是否引入内联展开表单或固定尺寸模态。
- 既有违规页通过 FR-188 全站审计逐一改造（行为不变）。
