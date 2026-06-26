/**
 * 设置表单纯函数助手（FR-158）。
 * 草稿 vs 当前值的脏数据比对——抽为纯函数便于 vitest 覆盖，供切分类未保存拦截与保存按钮态使用。
 */

/** 比对所需的最小配置项形态（键 + 当前生效值）。 */
export interface DraftDiffItem {
  key: string
  value: string
}

/**
 * 计算草稿相对当前值的有效改动集（仅含真正不同的键）。
 * 草稿里等于当前值、未定义、或不在可编辑项集合内的键一律剔除。
 */
export function diffSettings(
  items: DraftDiffItem[],
  draft: Record<string, string>,
): Record<string, string> {
  const changed: Record<string, string> = {}
  for (const it of items) {
    const v = draft[it.key]
    if (v !== undefined && v !== it.value) changed[it.key] = v
  }
  return changed
}

/** 是否存在未保存改动（切分类前据此决定是否拦截）。 */
export function hasUnsavedChanges(items: DraftDiffItem[], draft: Record<string, string>): boolean {
  return Object.keys(diffSettings(items, draft)).length > 0
}
