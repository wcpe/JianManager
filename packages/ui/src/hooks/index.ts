/**
 * a11y hooks 出口（FR-496 阶段 6）。
 *
 * 这些 hook 只服务「不基于 Radix 的自绘浮层与滚动区」；Radix 组合件（Dialog/Sheet/
 * DropdownMenu）自带宽高与焦点管理，不要重复套用——叠两套陷阱会让 ESC 与 Tab 的归属
 * 变得不可预测。
 */
export {
  FOCUSABLE_SELECTOR,
  attemptFocus,
  getFocusableElements,
  hasFocusableContent,
  isVisiblyFocusable,
} from './focusable'
export { useFocusTrap, type UseFocusTrapOptions, type UseFocusTrapReturn } from './useFocusTrap'
export {
  useScrollableArea,
  type ScrollableElementProps,
  type ScrollAreaState,
  type ScrollAxis,
  type ScrollKeyboardAccess,
  type UseScrollableAreaOptions,
  type UseScrollableAreaResult,
} from './useScrollableArea'
