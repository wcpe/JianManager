import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createPortal } from 'react-dom'
import { cn } from '@jianmanager/ui/lib/utils'
import type { InstanceInfo } from '@/api/instances'
// 类型与纯函数已抽到独立模块（避免本组件文件导出非组件值）。此处再导出，既有 import 路径不变。
export type { ContextMenuAction } from '@/components/views/instances/instanceContextMenuActions'

/**
 * 实例行右键菜单：**全表共用一个实例**（用户提出的「右键菜单」）。
 *
 * 【为什么不做成每行一棵 Radix ContextMenu】这个页面已经付过一次学费：健康度格原先每行包一个
 * Radix Popover，50 行就是 50 套 Root/Trigger/Popper 上下文，实测单次排序触发 300 次行渲染、
 * 主线程阻塞 880ms。右键菜单若按行挂载，成本与行数线性相关，且绝大多数行永远不会被右键。
 *
 * 【为什么不用 Radix DropdownMenu】这里需要「跟着鼠标坐标定位」而不是「跟着某个触发器定位」。
 * 用 DropdownMenu 得造一个 0 尺寸的虚拟锚点再算碰撞，代码更绕、还会额外引入一套 Presence/Popper；
 * 而菜单内容就是一组固定按钮，自己画反而更直接，也便于把键盘焦点与 Esc 收在一处。
 *
 * 【交互约定】
 * - 打开即聚焦第一项，Esc / 点击外部关闭（与桌面右键菜单同手感）；
 * - 视口边缘翻转：贴近右/下边时向左/上展开，避免菜单被裁；
 * - 危险项（删除）单独分隔并标红，与「⋯」菜单的信息层级保持一致。
 */
export function InstanceContextMenu({
  inst,
  position,
  actions,
  onClose,
}: {
  inst: InstanceInfo | null
  /** 光标坐标（视口坐标系）。 */
  position: { x: number; y: number } | null
  actions: ContextMenuAction[]
  onClose: () => void
}) {
  const { t } = useTranslation()
  const menuRef = useRef<HTMLDivElement | null>(null)
  const [placement, setPlacement] = useState<{ left: number; top: number } | null>(null)

  /*
    先按「向右下展开」渲染一帧拿到真实尺寸，再据视口边界翻转/贴边。
    要用 useLayoutEffect 而不是 useEffect：后者在浏览器绘制之后才跑，用户会看到菜单先出现在
    错误位置再跳一下。布局阶段算完，首帧就是最终位置。
  */
  useLayoutEffect(() => {
    if (!position || !menuRef.current) {
      setPlacement(null)
      return
    }
    const rect = menuRef.current.getBoundingClientRect()
    const margin = 8
    const left =
      position.x + rect.width + margin > window.innerWidth
        ? Math.max(margin, position.x - rect.width)
        : position.x
    const top =
      position.y + rect.height + margin > window.innerHeight
        ? Math.max(margin, position.y - rect.height)
        : position.y
    setPlacement({ left, top })
  }, [position, actions.length])

  // 打开即聚焦首项：键盘用户 Tab 进来能直接上下选，不用先 Tab 过整张表。
  useEffect(() => {
    if (!position) return
    const first = menuRef.current?.querySelector<HTMLButtonElement>('[data-menu-item]:not([disabled])')
    first?.focus()
  }, [position])

  // Esc 关闭 + 点击外部关闭（捕获阶段，先于行点击的冒泡处理）。
  useEffect(() => {
    if (!position) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onClose()
      }
    }
    const onPointerDown = (event: PointerEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) onClose()
    }
    document.addEventListener('keydown', onKey, true)
    document.addEventListener('pointerdown', onPointerDown, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      document.removeEventListener('pointerdown', onPointerDown, true)
    }
  }, [position, onClose])

  if (!position || !inst) return null

  const primary = actions.filter((a) => !a.danger)
  const danger = actions.filter((a) => a.danger)

  const renderItem = (action: ContextMenuAction) => (
    <button
      key={action.key}
      type="button"
      role="menuitem"
      data-menu-item
      disabled={action.disabled}
      title={action.disabled ? action.disabledHint : undefined}
      onClick={() => {
        if (action.disabled) return
        action.onSelect()
        onClose()
      }}
      className={cn(
        'flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs outline-none',
        'hover:bg-accent focus-visible:bg-accent',
        action.danger ? 'text-status-danger' : 'text-popover-foreground',
        action.disabled && 'cursor-not-allowed opacity-50 hover:bg-transparent',
      )}
    >
      <span className="flex size-3.5 shrink-0 items-center justify-center [&_svg]:size-3.5">
        {action.icon}
      </span>
      <span className="min-w-0 flex-1 truncate">{action.label}</span>
    </button>
  )

  return createPortal(
    <div
      ref={menuRef}
      role="menu"
      aria-label={t('instances.rowContextMenu', '「{{name}}」实例操作', { name: inst.name })}
      data-testid="instance-context-menu"
      className={cn(
        'fixed z-50 min-w-40 rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
        // placement 未算出前先隐藏，避免首帧闪在错误坐标
        placement ? 'visible' : 'invisible',
      )}
      style={{ left: placement?.left ?? position.x, top: placement?.top ?? position.y }}
    >
      <div className="truncate border-b border-border/60 px-2 pb-1 mb-1 text-[11px] text-muted-foreground">
        {inst.name}
      </div>
      {primary.map(renderItem)}
      {danger.length > 0 && <div className="my-1 h-px bg-border" />}
      {danger.map(renderItem)}
    </div>,
    document.body,
  )
}
