/**
 * 创建/编辑模态框高度自适应壳（FR-072）。
 *
 * 统一两类模态框的「视口约束 + 内部滚动」：
 * - shadcn `<Dialog>` 系：用 {@link scrollableDialogContentClass} 约束 DialogContent，
 *   并把可滚动正文包进 {@link ScrollableDialogBody}（头/脚不滚、仅正文滚）。
 * - 裸 `<div className="fixed inset-0 ...">` 系：复用 {@link MODAL_OVERLAY} / {@link MODAL_PANEL}
 *   常量类，使面板受 `max-h-[88vh] + overflow-y-auto`，短视口可上下滚动。
 */
import * as React from 'react'
import { cn } from '@/lib/utils'

/**
 * shadcn DialogContent 的高度约束类：限高到视口内并改为纵向 flex 布局，
 * 配合 {@link ScrollableDialogBody} 让头/脚固定、正文滚动。
 * 接在 `<DialogContent className={cn(scrollableDialogContentClass, '其它')}>`。
 */
export const scrollableDialogContentClass = 'flex max-h-[calc(100dvh-4rem)] flex-col'

/**
 * 可滚动正文区：占据 DialogContent 剩余高度并在内容超高时滚动；
 * 负 margin + padding 抵消 DialogContent 的内边距，避免滚动条贴边裁切焦点环。
 */
export function ScrollableDialogBody({
  className,
  ...props
}: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="scrollable-dialog-body"
      className={cn(
        '-mx-6 min-h-0 flex-1 overflow-y-auto px-6',
        className,
      )}
      {...props}
    />
  )
}

/** 裸 div 模态框遮罩层类（居中 + 暗背景 + 短视口留白可点空白关闭区）。 */
export const MODAL_OVERLAY =
  'fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4'

/**
 * 裸 div 模态框面板类：受 `max-h-[88vh] + overflow-y-auto` 约束，短视口内部滚动而非顶满屏。
 * 宽度按需在调用处追加（如 `max-w-md`）。
 */
export const MODAL_PANEL =
  'bg-background border rounded-lg p-6 w-full shadow-lg max-h-[88vh] overflow-y-auto'
