/**
 * Node.js 主版本清单（下拉选项的单一来源）。
 *
 * 【为什么提到 lib 而不是留在某个组件里】安装 Node.js 有两个入口（抽屉的「安装 Node.js」
 * 段、运行时列表里的安装按钮）。版本清单若各留一份，两个入口迟早给出不同的选项——
 * 那是用户最容易发现、也最没道理的不一致。这里只有一份。
 *
 * 【为什么 EOL 塞进 label】`ComboboxOption` 只有 value/label 两个字段（没有副标题位）。
 * 而「维护到什么时候」是选版本的硬约束，不能只放在 tooltip 里——悬停才可见等于没有。
 * 于是并进 label 的一行文本：`Node.js 22 · LTS Jod · 当前推荐 · 维护至 2027-04`。
 */
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'

/** 一个 Node.js 主版本的决策信息。 */
export interface NodeMajorInfo {
  major: number
  /** LTS 代号；null 表示 Current（非 LTS）。 */
  lts: string | null
  /** 维护截止（YYYY-MM）。 */
  eol: string
  /** 额外说明，如「当前推荐」「已接近 EOL」。 */
  note?: string
}

export const NODE_MAJORS: readonly NodeMajorInfo[] = [
  { major: 22, lts: 'Jod', eol: '2027-04', note: '当前推荐' },
  { major: 20, lts: 'Iron', eol: '2026-04' },
  { major: 18, lts: 'Hydrogen', eol: '2025-04', note: '已接近 EOL' },
  { major: 24, lts: null, eol: '2028-04', note: 'Current（非 LTS）' },
]

/** 主版本 → 下拉选项（带 LTS 代号、推荐标记与维护截止）。 */
export const NODE_MAJOR_OPTIONS: readonly ComboboxOption[] = NODE_MAJORS.map((m) => ({
  value: String(m.major),
  label: `Node.js ${m.major} · ${m.lts ? `LTS ${m.lts}` : 'Current'}${m.note ? ` · ${m.note}` : ''} · 维护至 ${m.eol}`,
}))

/** LTS 主版本号列表（默认选中项用）。 */
export const NODE_LTS_MAJORS: readonly number[] = NODE_MAJORS.filter((m) => m.lts).map((m) => m.major)

/**
 * 某个主版本的发行通道描述：「LTS Jod」/「Current」。
 *
 * 【为什么抽成函数】安装预览要在两处显示这个串（抽屉的 Node 段、列表里的安装入口）。
 * 之前两处各写一遍三元表达式，其中一处还把 `find()` 调了两次（一次判空、一次带 `!` 取值）——
 * 同一件事两份实现，正是"同一个版本在两个入口显示得不一样"的来源。
 */
export function nodeChannelLabel(major: number | string): string {
  const info = NODE_MAJORS.find((m) => String(m.major) === String(major))
  if (!info) return ''
  return info.lts ? `LTS ${info.lts}` : 'Current'
}
