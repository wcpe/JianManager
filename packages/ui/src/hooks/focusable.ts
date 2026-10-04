/**
 * 焦点可达性判定（FR-496 阶段 6）：把「哪些元素算可聚焦」收敛成一处。
 *
 * 背景：焦点陷阱与滚动区域可达性都要回答同一个问题——「这个容器里有没有可 Tab 到的东西」。
 * 两处各自写选择器必然走偏（一个只认 button/input，另一个还认 contenteditable），
 * 于是同一棵 DOM 在两个 hook 眼里对「有没有可聚焦元素」给出不同答案。
 *
 * 与 Astryx `hooks/focusableSelector.ts` 的对应关系：选择器取同一份语义清单
 * （含 media[controls] / iframe / 展开的 details>summary 这类朴素选择器会漏掉的成员），
 * 但把「可见可聚焦」的判定拆成独立函数，使陷阱与滚动区共用同一套过滤规则。
 */

/**
 * 可聚焦元素选择器（不含 tabIndex=-1 的编程聚焦点）。
 *
 * 除了 button/a/input/select/textarea 这些显然的成员，还必须包含：
 * - `[contenteditable]`：可编辑区域是真 Tab 停靠点；
 * - `audio/video[controls]`、`iframe`：浏览器同样把它们放进 Tab 序列；
 * - `details > summary:first-child`：折叠面板展开后 summary 可聚焦。
 * 少了任何一类，焦点陷阱就会在「唯一交互内容是（例如）可编辑区」时漏掉它，
 * 让 Tab 直接逃到遮罩背后的页面。
 */
export const FOCUSABLE_SELECTOR = [
  'button:not([disabled])',
  'a[href]',
  'area[href]',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
  '[contenteditable]:not([contenteditable="false"])',
  'audio[controls]',
  'video[controls]',
  'iframe',
  'details > summary:first-child',
].join(', ')

/**
 * 元素是否「真的」可聚焦（浏览器会为它停下 Tab）。
 *
 * 只看选择器是不够的：被 `hidden`/`inert`/`aria-hidden="true"` 包住、或经
 * `display:none`/`visibility:hidden` 隐藏的元素依然匹配选择器，但浏览器会跳过它们。
 * 其中 `aria-hidden="true"` 那一类尤其危险——视障用户读不到、键盘用户却 Tab 得到
 * （WCAG 4.1.2：可聚焦内容必须对辅助技术可见），所以它必须被排除。
 */
export function isVisiblyFocusable(element: HTMLElement): boolean {
  // 逐级向上检查：`display:none` 不是继承属性，计算样式也不会替你回溯祖先——
  // 只看元素自身，会把「父层被 display:none 收起」的可聚焦节点算成可 Tab，
  // 于是焦点陷阱把焦点送进一个屏幕上根本不存在的地方。
  for (let node: HTMLElement | null = element; node !== null; node = node.parentElement) {
    if (node.hidden || node.hasAttribute('inert')) return false
    // aria-hidden 子树里的可聚焦内容：辅助技术读不到、键盘却摸得到（WCAG 4.1.2）
    if (node.getAttribute('aria-hidden') === 'true') return false

    if (typeof window !== 'undefined' && typeof window.getComputedStyle === 'function') {
      const style = window.getComputedStyle(node)
      if (style.display === 'none') return false
      // visibility 可被后代用 visible 覆盖，故只在元素自身上判定（其计算值已含继承）
      if (node === element && style.visibility === 'hidden') return false
    }
  }
  return true
}

/**
 * 收集容器内全部可见可聚焦元素，按文档顺序返回。
 *
 * 用 `querySelectorAll` 的文档顺序而非自己排序：Tab 序列就是文档顺序。
 */
export function getFocusableElements(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    isVisiblyFocusable,
  )
}

/** 容器内是否至少有一个可见可聚焦元素（含容器自身可聚焦的情况）。 */
export function hasFocusableContent(container: HTMLElement): boolean {
  if (container.matches(FOCUSABLE_SELECTOR) && isVisiblyFocusable(container)) return true
  return getFocusableElements(container).length > 0
}

/**
 * 把焦点移到元素上，返回是否成功。
 *
 * 不用 `element.focus()` 的返回值（它恒为 undefined），而以 `document.activeElement`
 * 为准：被 inert/只读/不可见挡住时 focus() 是静默失败的，调用方必须知道失败才能继续找下一个。
 */
export function attemptFocus(element: HTMLElement): boolean {
  try {
    element.focus()
  } catch {
    // 少数元素（或在某些浏览器下已脱离文档的元素）focus() 会抛，按失败处理
  }
  return document.activeElement === element
}
