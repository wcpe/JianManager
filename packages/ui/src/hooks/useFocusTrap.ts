import * as React from 'react'

import { attemptFocus, getFocusableElements } from './focusable'

/**
 * 焦点陷阱（FR-496 阶段 6）：把键盘焦点关在一个容器内，关闭时把焦点还回去。
 *
 * 用途：不基于 Radix 的自绘模态/抽屉（Radix `Dialog`/`Sheet` 自带陷阱，不要重复套用）。
 *
 * 与 Astryx `useFocusTrap` 的对应关系与差异：
 * - 相同：容器 ref + `focusFirst()`；Tab/Shift+Tab 在首尾环绕；焦点被键鼠带出容器时的处理；
 *   关闭时只在「焦点确实进过陷阱、且此刻已丢失」时才回收（见下）。
 * - 不同：Astryx 把 ESC 交给共享图层栈（`useLayerDismissal`）统一裁决，谁在最上层谁消费；
 *   本库暂无图层栈，故 ESC 由可选的 `onEscape` 直接接管——**不传就完全不碰 ESC**，
 *   这样嵌在 Radix 图层里时不会出现「一次 ESC 关两层」。
 *
 * 不实现的：滚动锁定、遮罩背景、aria-modal。那些属于模态外壳的职责，不是焦点陷阱的。
 */

export interface UseFocusTrapOptions {
  /** 陷阱是否生效（弹窗打开时为 true）。 */
  isActive: boolean
  /**
   * ESC 回调；不传则本 hook 不接管 ESC。
   * 嵌在 Radix 图层（Dialog/Sheet/DropdownMenu）里时应省略，交给图层自己裁决。
   */
  onEscape?: () => void
  /**
   * 失效时是否把焦点还给激活前的元素，默认 true。
   *
   * 只在「焦点原本在陷阱内、且此刻已经无处可去」时才回收：
   * 若调用点或用户已经把焦点移到别处（点了页面上另一个控件、或调用点自己做了还原），
   * 无条件回收会把焦点抢回来——这是浮层关闭后"焦点莫名跳回旧位置"的常见根因。
   */
  restoreFocus?: boolean
}

export interface UseFocusTrapReturn<T extends HTMLElement = HTMLElement> {
  /** 挂到作为陷阱边界的容器元素上。 */
  containerRef: React.RefObject<T | null>
  /** 把焦点移到容器内第一个可聚焦元素；返回是否移动成功。 */
  focusFirst: () => boolean
  /** 把焦点移到容器内最后一个可聚焦元素；返回是否移动成功。 */
  focusLast: () => boolean
}

export function useFocusTrap<T extends HTMLElement = HTMLElement>({
  isActive,
  onEscape,
  restoreFocus = true,
}: UseFocusTrapOptions): UseFocusTrapReturn<T> {
  const containerRef = React.useRef<T | null>(null)
  // 陷阱内的上一个焦点位置：焦点被键盘带到容器外时，用它决定是「回到第一个」还是「回到最后一个」
  const lastFocusRef = React.useRef<Element | null>(null)
  // 本次焦点移动是否由键盘触发。鼠标点出去不该被拽回来（那会让"点遮罩关闭"这类交互失灵），
  // 只把键盘 Tab 出去的情况拉回容器内。
  const isKeyboardNavRef = React.useRef(false)

  // 回调存进 ref：调用点常常传内联箭头函数，若直接进 deps，每次渲染都会卸载重装 document 监听器
  const onEscapeRef = React.useRef(onEscape)
  React.useEffect(() => {
    onEscapeRef.current = onEscape
  }, [onEscape])

  const focusFirst = React.useCallback((): boolean => {
    const container = containerRef.current
    if (container === null) return false
    for (const element of getFocusableElements(container)) {
      if (attemptFocus(element)) return true
    }
    return false
  }, [])

  const focusLast = React.useCallback((): boolean => {
    const container = containerRef.current
    if (container === null) return false
    const focusable = getFocusableElements(container)
    for (let index = focusable.length - 1; index >= 0; index -= 1) {
      if (attemptFocus(focusable[index])) return true
    }
    return false
  }, [])

  // ① Tab / Shift+Tab：在首尾环绕，焦点落在容器自身时先进入内容
  React.useEffect(() => {
    if (!isActive) return

    const handleKeyDown = (event: KeyboardEvent) => {
      const container = containerRef.current
      if (container === null) return

      if (event.key === 'Escape') {
        // 只有调用点显式接管时才消费；不 stopPropagation，让上层图层仍能收到这次按键
        onEscapeRef.current?.()
        return
      }

      if (event.key !== 'Tab') return
      isKeyboardNavRef.current = true

      const focusable = getFocusableElements(container)
      if (focusable.length === 0) {
        const active = document.activeElement
        if (active !== null && !container.contains(active) && active !== container) {
          // 焦点本来就不在陷阱里（锚定型浮层把焦点留在触发元素上）：此时拦 Tab 会夺走整页的键盘操作
          isKeyboardNavRef.current = false
          return
        }
        // 容器内没有任何 Tab 停靠点（例如只有一段说明文字，焦点在 tabIndex=-1 的标题上）：
        // 把焦点钉住，别让它溜进视觉上已被遮罩盖住的背景页面
        event.preventDefault()
        lastFocusRef.current = active
        isKeyboardNavRef.current = false
        return
      }

      const first = focusable[0]
      const last = focusable[focusable.length - 1]

      if (document.activeElement === container) {
        // 焦点在容器边界上（tabIndex=-1 的模态面板）：先进入内容，而不是直接离开
        event.preventDefault()
        if (event.shiftKey) {
          last.focus()
        } else {
          first.focus()
        }
        return
      }

      if (event.shiftKey) {
        if (document.activeElement === first) {
          event.preventDefault()
          last.focus()
        }
      } else if (document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [isActive])

  // ② 键盘把焦点带出容器时拉回来（capture 阶段拦截，抢在浏览器 settle 之前）
  React.useEffect(() => {
    if (!isActive) return

    const handleFocusIn = (event: FocusEvent) => {
      const container = containerRef.current
      if (container === null) return

      const target = event.target as Node | null
      if (target !== null && container.contains(target)) {
        lastFocusRef.current = target as Element
        isKeyboardNavRef.current = false
        return
      }

      if (!isKeyboardNavRef.current) return

      // 取「拉回之前」的焦点快照：focusFirst() 会触发一次嵌套 focusin，
      // 那时 lastFocusRef 已被改写成新焦点，用它判断环绕就会误判
      const focusBeforePull = lastFocusRef.current
      const movedToFirst = focusFirst()
      // Shift+Tab 从第一个元素往回退时会回到同一个元素，此时应改去最后一个，形成环绕
      if (movedToFirst && focusBeforePull === document.activeElement) {
        focusLast()
      }
      lastFocusRef.current = document.activeElement
      isKeyboardNavRef.current = false
    }

    document.addEventListener('focusin', handleFocusIn, true)
    return () => document.removeEventListener('focusin', handleFocusIn, true)
  }, [isActive, focusFirst, focusLast])

  // ③ 记住激活前的焦点，失效时按需回收
  React.useEffect(() => {
    if (!isActive) return

    const previouslyFocused = document.activeElement as HTMLElement | null
    // 容器此刻可能还没挂载（同一提交里新建），故清理期再取一次
    const container = containerRef.current

    // 焦点是否真的进过陷阱。锚定型浮层（下拉/气泡）故意把焦点留在触发元素上，
    // 对它们回收焦点会撤销用户主动触发的那次 blur，导致"再点一次打不开"。
    let hadFocusInside = container !== null && container.contains(document.activeElement)
    const markFocusInside = (event: FocusEvent) => {
      const target = event.target as Node | null
      if (target !== null && containerRef.current?.contains(target)) hadFocusInside = true
    }
    document.addEventListener('focusin', markFocusInside, true)

    return () => {
      document.removeEventListener('focusin', markFocusInside, true)
      if (!restoreFocus || !hadFocusInside) return

      const active = document.activeElement
      const focusWasLost =
        active === null ||
        active === document.body ||
        active === document.documentElement ||
        active === container ||
        (container !== null && container.contains(active))

      if (!focusWasLost) return
      if (previouslyFocused !== null && previouslyFocused.isConnected) {
        attemptFocus(previouslyFocused)
      }
    }
  }, [isActive, restoreFocus])

  return { containerRef, focusFirst, focusLast }
}
