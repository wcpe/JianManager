import type { BusinessAction } from '@/api/business'

/**
 * 业务动作的纯函数判定（FR-121，见 ADR-029）。抽成独立模块便于单测，
 * 且不污染 BusinessSegment 组件文件的 fast-refresh（react-refresh/only-export-components）。
 */

/** 判定一个业务动作是否为写动作（manifest readOnly 缺省视为写，从严）。 */
export function isWriteAction(action: BusinessAction): boolean {
  return action.readOnly !== true
}
