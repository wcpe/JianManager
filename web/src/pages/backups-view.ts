/**
 * 备份页纯逻辑助手（FR-151）。汇总统计、增量链父子关系、删除依赖计数、进行中轮询判定——
 * 抽为纯函数便于 vitest 覆盖，UI 组件只做渲染。
 *
 * 备份状态码与后端 model.BackupStatus 对齐：0 待处理 / 1 进行中 / 2 完成 / 3 失败。
 * 备份模式与 model.BackupMode 对齐：0 全量 / 1 增量。
 */
import type { StatusLevel } from '@/lib/threshold'

/** 备份状态码。 */
export const BACKUP_PENDING = 0
export const BACKUP_IN_PROGRESS = 1
export const BACKUP_COMPLETED = 2
export const BACKUP_FAILED = 3

/** 备份模式码。 */
export const BACKUP_MODE_FULL = 0
export const BACKUP_MODE_INCREMENTAL = 1

/** 视图渲染所需的最小备份形状（与 api/backups 的 BackupInfo 字段子集对齐）。 */
export interface BackupLike {
  id: number
  fileSizeMb: number
  mode: number
  status: number
  parentId?: number
  createdAt: string
}

/** 备份状态码 → i18n 名称 key（backups 命名空间）。 */
export function backupStatusKey(status: number): string {
  switch (status) {
    case BACKUP_PENDING:
      return 'pending'
    case BACKUP_IN_PROGRESS:
      return 'inProgress'
    case BACKUP_COMPLETED:
      return 'completed'
    case BACKUP_FAILED:
      return 'failed'
    default:
      return 'pending'
  }
}

/** 备份状态码 → 状态等级（StatusBadge 着色）：完成=正常，进行中/待处理=信息/警告，失败=危险。 */
export function backupStatusLevel(status: number): StatusLevel {
  switch (status) {
    case BACKUP_COMPLETED:
      return 'success'
    case BACKUP_IN_PROGRESS:
      return 'info'
    case BACKUP_PENDING:
      return 'warning'
    case BACKUP_FAILED:
      return 'danger'
    default:
      return 'neutral'
  }
}

/** 状态是否为「进行中/待处理」——存在此类备份时需轮询刷新进度（FR-151）。 */
export function isActiveStatus(status: number): boolean {
  return status === BACKUP_PENDING || status === BACKUP_IN_PROGRESS
}

/** 列表中存在进行中/待处理备份时返回 true，用于驱动条件轮询（FR-151）。 */
export function hasActiveBackup(backups: readonly BackupLike[]): boolean {
  return backups.some((b) => isActiveStatus(b.status))
}

/** 备份汇总统计（FR-151）：总占用 MB、份数、最近一次成功完成时间（ISO，无则 undefined）。 */
export interface BackupSummary {
  totalSizeMb: number
  count: number
  lastSuccessAt?: string
}

/**
 * 计算备份汇总（FR-151）：份数=全部记录，总占用=各 fileSizeMb 之和（仅累加有限值），
 * 最近成功=已完成备份里 createdAt 最大者。
 */
export function summarizeBackups(backups: readonly BackupLike[]): BackupSummary {
  let totalSizeMb = 0
  let lastSuccessAt: string | undefined
  for (const b of backups) {
    if (Number.isFinite(b.fileSizeMb)) totalSizeMb += b.fileSizeMb
    if (b.status === BACKUP_COMPLETED) {
      if (!lastSuccessAt || b.createdAt > lastSuccessAt) lastSuccessAt = b.createdAt
    }
  }
  return { totalSizeMb, count: backups.length, lastSuccessAt }
}

/**
 * 统计直接以某备份为父的增量备份数（FR-151）——删除父备份前据此警告「N 个增量依赖」。
 * 仅计直接子级（链上更深的孙级会随父删触发级联，由后端 deleteHasChildren 拦截）。
 */
export function countDependents(backups: readonly BackupLike[], parentId: number): number {
  return backups.reduce((n, b) => (b.parentId === parentId ? n + 1 : n), 0)
}

/** 该备份是否为增量且挂在某父备份上（用于行内展示父备份关系）。 */
export function isIncrementalChild(b: BackupLike): boolean {
  return b.mode === BACKUP_MODE_INCREMENTAL && b.parentId !== undefined && b.parentId !== null
}

/** 人类可读的字节大小（输入为 MB）：<1024MB 显 MB，≥1024MB 显 GB，保留一位小数。 */
export function formatSizeMb(sizeMb: number): string {
  if (!Number.isFinite(sizeMb) || sizeMb < 0) return '0.0 MB'
  if (sizeMb >= 1024) return `${(sizeMb / 1024).toFixed(1)} GB`
  return `${sizeMb.toFixed(1)} MB`
}
