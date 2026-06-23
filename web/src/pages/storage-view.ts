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
