/**
 * 可编辑下拉框（Combobox）的纯逻辑（FR-072）。
 *
 * Combobox = 已知集下拉 + 允许自定义输入。这里只放与 DOM 无关的过滤/匹配逻辑，
 * 便于单测；渲染交互在 components/ui/combobox.tsx。
 */

/** 下拉选项：value 为提交值，label 为展示文本（缺省用 value）。 */
export interface ComboboxOption {
  value: string
  label?: string
}

/** 选项的展示文本（label 优先，回退 value）。 */
export function optionLabel(opt: ComboboxOption): string {
  return opt.label ?? opt.value
}

/**
 * 按输入串过滤选项：大小写不敏感，匹配 value 或 label 的子串。
 * 输入为空时返回全部选项（展示完整已知集）。
 */
export function filterOptions(options: ComboboxOption[], query: string): ComboboxOption[] {
  const q = query.trim().toLowerCase()
  if (q === '') return options
  return options.filter((o) => {
    const v = o.value.toLowerCase()
    const l = (o.label ?? '').toLowerCase()
    return v.includes(q) || l.includes(q)
  })
}

/** 当前输入是否与某个已知选项的 value 完全相等（用于判定「自定义值」徽标）。 */
export function isKnownValue(options: ComboboxOption[], value: string): boolean {
  return options.some((o) => o.value === value)
}

/**
 * 是否应展示「添加自定义项」：输入非空、且与任何已知 value 都不完全相等。
 * 允许自定义输入的 Combobox 据此在列表底部给出「使用 "xxx"」入口。
 */
export function shouldOfferCustom(options: ComboboxOption[], query: string): boolean {
  const q = query.trim()
  if (q === '') return false
  return !options.some((o) => o.value === q)
}

/** 把字符串数组归一为选项列表（value=label=元素）。 */
export function toOptions(values: readonly string[]): ComboboxOption[] {
  return values.map((v) => ({ value: v }))
}
