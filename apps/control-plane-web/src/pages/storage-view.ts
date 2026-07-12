import type { ArchiveSummary, DirUsage } from '@/api/storage'

/**
 * 平台存储资源管理器（FR-083）的纯展示逻辑：字节格式化、归档冷热汇总、目录占用排序。
 * 抽成无 React 依赖的模块以便 vitest 单测（参照 runtime-assets-view.ts 约定）。
 */

/** 人类可读字节（1024 进制）。负数/非有限按 0 处理。 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  // 整数位用整数，否则保留一位小数。
  const text = v >= 100 || Number.isInteger(v) ? Math.round(v).toString() : v.toFixed(1)
  return `${text} ${units[i]}`
}

/** 归档冷热汇总：资产总数、冷数据（归档+外置）数、冷数据占用字节。 */
export interface ArchiveDerived {
  total: number
  cold: number
  coldSize: number
}

/** 由归档分布派生汇总（资产总数 / 冷数据数 / 冷数据占用）。 */
export function deriveArchive(a: ArchiveSummary): ArchiveDerived {
  const cold = a.archivedCount + a.externalCount
  return {
    total: a.hotCount + cold,
    cold,
    coldSize: a.archivedSize + a.externalSize,
  }
}

/**
 * 目录占用排序：存在的目录在前（按占用降序），缺失目录置末（保留布局顺序由调用方传入的稳定性）。
 * 用于概览面板把「有内容的目录」突出在前，空/缺失目录沉底。
 */
export function sortDirsByUsage(dirs: DirUsage[]): DirUsage[] {
  return [...dirs].sort((x, y) => {
    if (x.exists !== y.exists) return x.exists ? -1 : 1
    return y.size - x.size
  })
}

/** 面包屑一段：展示名 + 跳转该层用的相对路径（根为空串）。 */
export interface Crumb {
  name: string
  path: string
}

/**
 * 由当前相对路径构造只读浏览面包屑。空路径仅含根段。
 * 各段 path 为「从根到该段」的累积相对路径（以「/」分隔，供 /storage/files?path= 直接使用）。
 * rootName 为根段展示名（由调用方传入已 i18n 文案）。
 */
export function buildCrumbs(rel: string, rootName: string): Crumb[] {
  const crumbs: Crumb[] = [{ name: rootName, path: '' }]
  const trimmed = rel.replace(/^\/+|\/+$/g, '')
  if (trimmed === '') return crumbs
  let acc = ''
  for (const seg of trimmed.split('/')) {
    if (seg === '') continue
    acc = acc === '' ? seg : `${acc}/${seg}`
    crumbs.push({ name: seg, path: acc })
  }
  return crumbs
}

/** 拼接父相对路径与子项名为新的相对路径（根为空串）。 */
export function joinStoragePath(parent: string, name: string): string {
  return parent === '' ? name : `${parent}/${name}`
}
