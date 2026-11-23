import { useEffect, useRef, useState } from 'react'

import { cn } from '../lib/utils'

/**
 * 表格表头右缘的列宽拖拽手柄。
 *
 * 只负责「拖动 → 回调宽度」，不知道列 key、上下限或存储方式：
 * 各表在回调里按自己的列模型 clamp 并持久化，避免把某一张表的规则塞进通用组件。
 *
 * 手柄跨在列边界上（右移半个手柄宽），命中区域比视觉线宽得多——
 * 用户不会精确点到 1px 的边线，太窄会表现成「拖不动」。
 * 双击恢复该列默认宽度。
 */
export function ColumnResizeHandle({
  label,
  title,
  testId,
  onResize,
  onResizeEnd,
  onReset,
}: {
  /** 可访问名（读屏念出的「拖动调整列宽」）。 */
  label: string
  /** 原生 tooltip（可写明双击恢复默认）。 */
  title: string
  testId?: string
  /** 拖动中的实时宽度（调用方负责 clamp）。 */
  onResize: (width: number) => void
  /** 松手：调用方在此落存储。 */
  onResizeEnd?: () => void
  /** 双击恢复默认宽度。 */
  onReset?: () => void
}) {
  const [dragging, setDragging] = useState(false)
  const listenersRef = useRef<{ move: (event: PointerEvent) => void; finish: () => void } | null>(null)

  const detach = () => {
    const current = listenersRef.current
    if (!current) return
    window.removeEventListener('pointermove', current.move)
    window.removeEventListener('pointerup', current.finish)
    window.removeEventListener('pointercancel', current.finish)
    listenersRef.current = null
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
  }

  // 拖动中被卸载（切页/关弹窗）时清理全局监听，避免继续改一个已不存在的表头。
  useEffect(() => () => detach(), [])

  const begin = (event: React.PointerEvent<HTMLSpanElement>) => {
    if (event.button !== 0) return
    const th = event.currentTarget.parentElement
    const startWidth = th?.getBoundingClientRect().width ?? 0
    if (startWidth <= 0) return

    event.preventDefault()
    event.stopPropagation()
    setDragging(true)
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'

    const startX = event.clientX
    const move = (next: PointerEvent) => {
      onResize(startWidth + next.clientX - startX)
    }
    const finish = () => {
      detach()
      setDragging(false)
      onResizeEnd?.()
    }
    listenersRef.current = { move, finish }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', finish)
    window.addEventListener('pointercancel', finish)
  }

  return (
    <span
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      title={title}
      data-testid={testId}
      data-resizing={dragging ? 'true' : undefined}
      draggable={false}
      onPointerDown={begin}
      onDragStart={(event) => {
        // 手柄在可拖拽的 th 内部：必须拦住 dragstart，否则按下去会变成「拖动整列换位」。
        event.preventDefault()
        event.stopPropagation()
      }}
      onClick={(event) => {
        event.preventDefault()
        event.stopPropagation()
      }}
      onDoubleClick={(event) => {
        event.preventDefault()
        event.stopPropagation()
        onReset?.()
      }}
      className="group/resize absolute inset-y-0 -right-0.5 z-30 flex w-2.5 cursor-col-resize touch-none select-none items-center justify-center"
    >
      <span
        aria-hidden="true"
        className={cn(
          'h-4 w-0.5 rounded-full transition-colors',
          dragging
            ? 'bg-primary'
            : 'bg-border opacity-60 group-hover/resize:bg-primary group-hover/resize:opacity-100',
        )}
      />
    </span>
  )
}
