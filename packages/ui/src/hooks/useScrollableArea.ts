import * as React from 'react'

import { attemptFocus, getFocusableElements, hasFocusableContent } from './focusable'

/**
 * 滚动区域的键盘可达性（FR-496 阶段 6）：让「只能滚、不能 Tab」的容器对键盘用户可用。
 *
 * 要解决的问题：一个 `overflow-auto` 的容器，若内容里全是纯文本/只读节点，
 * 键盘用户既进不去也滚不动——WCAG 2.1.1 意义上的"不可操作"。表格正文、
 * 日志查看器、模态框的滚动正文都属于这一类。
 *
 * 与 Astryx `useScrollableArea` 的对应关系与差异：
 * - 相同：`contentOrViewport` 的核心判断——内容自带 Tab 停靠点时容器不再多占一个停靠点，
 *   没有停靠点时才把容器本身变成可聚焦区域；`getViewportProps` / `getContentProps` 的
 *   取值器形态（调用方 props 与行为自有 props 在合并时互不覆盖）；边缘状态（atStart/atEnd）
 *   作为对外可见状态暴露给 CSS 做滚动阴影。
 * - 不同：Astryx 自己实现了 Tab 在内容与容器之间的委托与"滚动所有者注册表"
 *   （多滚动区嵌套时判定谁该响应），本库暂不引入注册表——嵌套滚动区的键盘归属由
 *   浏览器原生行为决定（内层先滚，滚到底再冒泡给外层），自实现反而会与之打架。
 * - 不实现的：用 JS 复刻方向键/翻页键滚动并在滚动中实时测量。聚焦在容器上时浏览器
 *   原生就会滚（且尊重 `scroll-behavior`、惯性、滚动锚定），重复实现只会滚动两次。
 */

/** 键盘可达策略。 */
export type ScrollKeyboardAccess =
  /** 自动：内容没有 Tab 停靠点时容器自己成为停靠点，有则让给内容（默认）。 */
  | 'auto'
  /** 强制容器自己成为停靠点：容器自带键盘语义（如日志查看器、终端回显区）。 */
  | 'viewport'
  /** 容器永不成为停靠点：调用方保证内容可聚焦（如可编辑表格）。 */
  | 'content'

/** 关注的滚动轴：只判断该轴是否溢出，另一轴的状态恒为"不溢出"。 */
export type ScrollAxis = 'vertical' | 'horizontal'

/** 滚动状态：是否溢出、是否已到两端（供调用方做滚动阴影/按钮禁用）。 */
export interface ScrollAreaState {
  isScrollable: boolean
  atStart: boolean
  atEnd: boolean
}

/**
 * 取值器接受的 props：DOM 属性 + ref，并显式放行 `data-*`。
 *
 * 放行 `data-*` 是必须的：调用方要用 `data-testid` 定位、用 `data-*` 挂样式钩子，
 * 而它们会被 `{...getViewportProps()}` 原样透传。
 */
export type ScrollableElementProps<E extends HTMLElement> = React.HTMLAttributes<E> &
  React.RefAttributes<E> & {
    [dataAttr: `data-${string}`]: string | number | boolean | undefined
  }

export interface UseScrollableAreaOptions {
  /** 键盘可达策略，默认 `auto`。 */
  keyboardAccess?: ScrollKeyboardAccess
  /**
   * 容器成为键盘停靠点时暴露的可访问名。
   *
   * 强烈建议给：一个可聚焦却无名的 `role="region"`，屏幕阅读器只会念"区域"，
   * 用户不知道 Tab 进来会滚的是哪一块内容。缺省时仍会保留 Tab 可达性
   * （可达优先于命名），但不设 `role`，避免产出无名地标。
   */
  label?: string
  /** 可访问角色，默认 `region`（可命名地标）；只想当普通分组时用 `group`。 */
  role?: 'region' | 'group'
  /** 关注的滚动轴，默认 `vertical`。 */
  axis?: ScrollAxis
}

export interface UseScrollableAreaResult {
  /** 展开到滚动视口元素（外层 `overflow-*` 容器）上。 */
  getViewportProps<E extends HTMLElement>(
    props?: ScrollableElementProps<E>,
  ): ScrollableElementProps<E>
  /** 展开到内容元素（视口的直接子元素）上。 */
  getContentProps<E extends HTMLElement>(
    props?: ScrollableElementProps<E>,
  ): ScrollableElementProps<E>
  /** 当前滚动状态。 */
  state: ScrollAreaState
}

/** 未溢出时的初始状态。 */
const IDLE_STATE: ScrollAreaState = { isScrollable: false, atStart: true, atEnd: true }

/** 方向键的单步滚动量（约一行文本 + 行距）。 */
const LINE_STEP = 48

/** 合并多个 ref：调用方常要自己拿节点（读位置、做定位），hook 也要，不合并就会有一方静默丢失。 */
function mergeRefs<E extends HTMLElement>(
  ...refs: Array<React.Ref<E> | undefined>
): React.RefCallback<E> {
  return (node: E | null) => {
    for (const ref of refs) {
      if (ref === undefined || ref === null) continue
      if (typeof ref === 'function') {
        ref(node)
      } else {
        ;(ref as React.MutableRefObject<E | null>).current = node
      }
    }
  }
}

/** 读取某条轴上的溢出与边缘状态。 */
function readScrollState(viewport: HTMLElement, axis: ScrollAxis): ScrollAreaState {
  const horizontal = axis === 'horizontal'
  const viewportSize = horizontal ? viewport.clientWidth : viewport.clientHeight
  const contentSize = horizontal ? viewport.scrollWidth : viewport.scrollHeight
  const offset = horizontal ? viewport.scrollLeft : viewport.scrollTop

  // 容差 1px：滚动容器的尺寸取整在不同缩放/DPR 下会差一点，严格比较会得到「差 1px 也算溢出」
  const maxOffset = Math.max(0, contentSize - viewportSize)
  const isScrollable = maxOffset > 1
  return {
    isScrollable,
    atStart: offset <= 1,
    atEnd: !isScrollable || maxOffset - offset <= 1,
  }
}

function sameState(a: ScrollAreaState, b: ScrollAreaState): boolean {
  return (
    a.isScrollable === b.isScrollable && a.atStart === b.atStart && a.atEnd === b.atEnd
  )
}

/** 按轴滚动指定增量；极老的引擎没有 `scrollBy(options)` 时退回直接改 offset。 */
function scrollByOffset(viewport: HTMLElement, axis: ScrollAxis, delta: number): void {
  if (typeof viewport.scrollBy === 'function') {
    viewport.scrollBy(axis === 'horizontal' ? { left: delta } : { top: delta })
    return
  }
  if (axis === 'horizontal') {
    viewport.scrollLeft += delta
  } else {
    viewport.scrollTop += delta
  }
}

/** 按轴滚到绝对位置。 */
function scrollToOffset(viewport: HTMLElement, axis: ScrollAxis, offset: number): void {
  if (typeof viewport.scrollTo === 'function') {
    viewport.scrollTo(axis === 'horizontal' ? { left: offset } : { top: offset })
    return
  }
  if (axis === 'horizontal') {
    viewport.scrollLeft = offset
  } else {
    viewport.scrollTop = offset
  }
}

export function useScrollableArea({
  keyboardAccess = 'auto',
  label,
  role = 'region',
  axis = 'vertical',
}: UseScrollableAreaOptions = {}): UseScrollableAreaResult {
  const [viewport, setViewport] = React.useState<HTMLElement | null>(null)
  const [content, setContent] = React.useState<HTMLElement | null>(null)
  const [state, setState] = React.useState<ScrollAreaState>(IDLE_STATE)
  const [contentHasFocusables, setContentHasFocusables] = React.useState(false)

  // 用 state 存节点（而非 ref）是为了让测量副作用在节点挂载/替换后自动重跑
  const viewportRefCallback = React.useCallback((node: HTMLElement | null) => {
    setViewport((current) => (current === node ? current : node))
  }, [])
  const contentRefCallback = React.useCallback((node: HTMLElement | null) => {
    setContent((current) => (current === node ? current : node))
  }, [])

  // 合成 ref 缓存：每次渲染新建回调会让 React 在旧 ref 上写 null、新 ref 上写节点，
  // 于是 setViewport 被反复调用、测量副作用反复重跑。
  const composedViewportRefCache = React.useRef<{
    caller: React.Ref<HTMLElement> | undefined
    composed: React.RefCallback<HTMLElement>
  } | null>(null)
  const composedContentRefCache = React.useRef<{
    caller: React.Ref<HTMLElement> | undefined
    composed: React.RefCallback<HTMLElement>
  } | null>(null)

  const getViewportRef = React.useCallback(
    (caller: React.Ref<HTMLElement> | undefined) => {
      const cached = composedViewportRefCache.current
      if (cached !== null && cached.caller === caller) return cached.composed
      const composed = mergeRefs(caller, viewportRefCallback)
      composedViewportRefCache.current = { caller, composed }
      return composed
    },
    [viewportRefCallback],
  )

  const getContentRef = React.useCallback(
    (caller: React.Ref<HTMLElement> | undefined) => {
      const cached = composedContentRefCache.current
      if (cached !== null && cached.caller === caller) return cached.composed
      const composed = mergeRefs(caller, contentRefCallback)
      composedContentRefCache.current = { caller, composed }
      return composed
    },
    [contentRefCallback],
  )

  React.useEffect(() => {
    if (viewport === null || content === null) return

    const readGeometry = () => {
      const next = readScrollState(viewport, axis)
      setState((current) => (sameState(current, next) ? current : next))
    }
    // 内容里有没有 Tab 停靠点，只在结构/尺寸变化时重算：滚动中每帧跑 querySelectorAll
    // 在大表格上就是实打实的卡顿，而它跟滚动位置无关。
    const readContentFocusability = () => {
      const next = hasFocusableContent(content)
      setContentHasFocusables((current) => (current === next ? current : next))
    }

    readGeometry()
    readContentFocusability()

    viewport.addEventListener('scroll', readGeometry, { passive: true })
    window.addEventListener('resize', readGeometry)
    window.addEventListener('resize', readContentFocusability)

    const resizeObserver =
      typeof ResizeObserver === 'function' ? new ResizeObserver(readGeometry) : null
    resizeObserver?.observe(viewport)
    resizeObserver?.observe(content)

    // 只观察内容子树：视口自身的属性由本 hook 写入（data-scrollable / tabIndex 等），
    // 观察它会让"测量 → 写属性 → 再测量"形成自激循环。
    const mutationObserver =
      typeof MutationObserver === 'function'
        ? new MutationObserver(() => {
            readGeometry()
            readContentFocusability()
          })
        : null
    mutationObserver?.observe(content, {
      childList: true,
      subtree: true,
      characterData: true,
      attributes: true,
      attributeFilter: ['class', 'style', 'hidden', 'dir'],
    })

    return () => {
      viewport.removeEventListener('scroll', readGeometry)
      window.removeEventListener('resize', readGeometry)
      window.removeEventListener('resize', readContentFocusability)
      resizeObserver?.disconnect()
      mutationObserver?.disconnect()
    }
  }, [axis, content, viewport])

  // 容器是否该成为键盘停靠点：不溢出就没有滚动的必要，多一个停靠点只是噪音
  const isKeyboardRegion =
    state.isScrollable &&
    keyboardAccess !== 'content' &&
    (keyboardAccess === 'viewport' || !contentHasFocusables)

  const getViewportProps = React.useCallback(
    <E extends HTMLElement>(
      props: ScrollableElementProps<E> = {},
    ): ScrollableElementProps<E> => {
      const { ref: callerRef, onKeyDown: callerKeyDown, ...rest } = props as ScrollableElementProps<E> & {
        ref?: React.Ref<E>
        onKeyDown?: React.KeyboardEventHandler<E>
      }

      // 按键处理：调用方的处理器先跑（它 preventDefault 就等于把这次按键拿回去，
      // 例如上层实现了虚拟滚动）；否则由本 hook 按停靠点归属决定是否消费。
      const handleKeyboardDelegation = (event: React.KeyboardEvent<E>) => {
        callerKeyDown?.(event)
        if (event.defaultPrevented) return
        if (!isKeyboardRegion) return
        // 只处理"焦点就在容器本身"的按键。事件会从内容冒泡上来，而内容里的输入框
        // 收到方向键是光标移动——抢走它就是打字事故。
        if (event.target !== event.currentTarget) return

        const target = event.currentTarget as unknown as HTMLElement

        if (event.key === 'Tab') {
          // 容器与内容都可达时（keyboardAccess="viewport"），Tab 应先进入内容而不是直接离开
          const focusable = getFocusableElements(content ?? target)
          if (focusable.length === 0) return
          event.preventDefault()
          if (event.shiftKey) {
            attemptFocus(focusable[focusable.length - 1])
          } else {
            attemptFocus(focusable[0])
          }
          return
        }

        const pageStep = (axis === 'horizontal' ? target.clientWidth : target.clientHeight) * 0.9
        switch (event.key) {
          case 'ArrowDown':
            if (axis !== 'vertical') return
            event.preventDefault()
            scrollByOffset(target, axis, LINE_STEP)
            break
          case 'ArrowUp':
            if (axis !== 'vertical') return
            event.preventDefault()
            scrollByOffset(target, axis, -LINE_STEP)
            break
          case 'ArrowRight':
            if (axis !== 'horizontal') return
            event.preventDefault()
            scrollByOffset(target, axis, LINE_STEP)
            break
          case 'ArrowLeft':
            if (axis !== 'horizontal') return
            event.preventDefault()
            scrollByOffset(target, axis, -LINE_STEP)
            break
          case 'PageDown':
            event.preventDefault()
            scrollByOffset(target, axis, pageStep)
            break
          case 'PageUp':
            event.preventDefault()
            scrollByOffset(target, axis, -pageStep)
            break
          case 'Home':
            event.preventDefault()
            scrollToOffset(target, axis, 0)
            break
          case 'End':
            event.preventDefault()
            scrollToOffset(
              target,
              axis,
              axis === 'horizontal'
                ? target.scrollWidth - target.clientWidth
                : target.scrollHeight - target.clientHeight,
            )
            break
          default:
            break
        }
      }

      return {
        ...rest,
        ref: getViewportRef(callerRef as React.Ref<HTMLElement> | undefined) as React.Ref<E>,
        onKeyDown: handleKeyboardDelegation,
        'data-scroll-axis': axis,
        // 行为自有的 props 放在调用方 props 之后：可访问性与滚动语义归本 hook 所有，
        // 调用方想改这些（tabIndex / role / 边缘状态钩子）应当去调 hook 的入参，而不是覆盖 props。
        ...(state.isScrollable
          ? {
              'data-scrollable': 'true',
              'data-scroll-at-start': state.atStart ? 'true' : 'false',
              'data-scroll-at-end': state.atEnd ? 'true' : 'false',
            }
          : {}),
        ...(isKeyboardRegion
          ? {
              tabIndex: 0,
              // 没有名字就不设 role：可聚焦但无名的地方会让屏幕阅读器只念"区域"，等于噪音
              ...(label ? { role, 'aria-label': label } : {}),
            }
          : {}),
      }
    },
    [axis, content, getViewportRef, isKeyboardRegion, label, role, state],
  )

  const getContentProps = React.useCallback(
    <E extends HTMLElement>(
      props: ScrollableElementProps<E> = {},
    ): ScrollableElementProps<E> => {
      const { ref: callerRef, ...rest } = props as ScrollableElementProps<E> & {
        ref?: React.Ref<E>
      }
      return {
        ...rest,
        ref: getContentRef(callerRef as React.Ref<HTMLElement> | undefined) as React.Ref<E>,
        'data-scroll-content': 'true',
      }
    },
    [getContentRef],
  )

  return { getViewportProps, getContentProps, state }
}
