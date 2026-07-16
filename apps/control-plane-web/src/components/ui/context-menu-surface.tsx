import { useLayoutEffect, useRef, useState, type CSSProperties, type HTMLAttributes } from 'react'
import { createPortal } from 'react-dom'

/**
 * 右键菜单浮层基座：portal 到 document.body + 视口钳制。
 *
 * 为什么必须 portal：控制台/路由过渡壳带 transform / will-change（`jm-route-transition`、
 * `jm-console-content`），`position: fixed` 的包含块会被此类祖先劫持——菜单以祖先左上角
 * 为原点整体漂移（真机：终端/文件管理器右键菜单偏移约一个侧栏+页眉的距离）。
 * 为什么要钳制：光标贴右/下边缘时按菜单实际尺寸向左/上收（留 8px 边距），不溢出屏幕。
 */
export function ContextMenuSurface({
  x,
  y,
  style,
  children,
  ...rest
}: { x: number; y: number } & HTMLAttributes<HTMLDivElement>) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const margin = 8
    setPos({
      left: Math.max(margin, Math.min(x, window.innerWidth - el.offsetWidth - margin)),
      top: Math.max(margin, Math.min(y, window.innerHeight - el.offsetHeight - margin)),
    })
  }, [x, y])
  const mergedStyle: CSSProperties = { ...style, left: pos?.left ?? x, top: pos?.top ?? y }
  return createPortal(
    <div ref={ref} style={mergedStyle} {...rest}>
      {children}
    </div>,
    document.body,
  )
}
