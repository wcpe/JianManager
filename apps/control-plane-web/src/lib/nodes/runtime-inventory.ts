/**
 * 运行时清单的合并视图（JDK 与其它运行时合成一张表）。
 *
 * 【为什么合并】改造前「JDK 管理」和「运行时」是两个并列面板，而后者在
 * `NodeRuntimeSection` 里第一件事就是 `filter(rt => rt.type !== 'jdk')`，
 * 把同一批数据强拆成互斥的两半。用户装 JDK 要在一个面板找、装 Node 要去另一个，
 * 而两者在语义上是同一件事（这台机器上有哪些可执行运行时、谁在用、能不能删）。
 * 合并成一张表后，那条 filter 补丁自然消失。
 */
import type { JDKRefInstance } from '@/api/jdks'

/** 合并行的类型（jdk / nodejs / python…）。 */
export type RuntimeRowType = 'jdk' | 'nodejs' | 'python' | (string & {})

/** 单条运行时（合并视图的行）。 */
export interface RuntimeInventoryRow {
  /** 稳定 key：类型 + 承载表 id（两表 id 可重复，不能只用 id）。 */
  key: string
  /** 承载表主键。 */
  id: number
  type: RuntimeRowType
  /** 展示名：JDK=厂商，其它=登记名。 */
  name: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  /** 引用实例（仅 JDK 有；其它类型恒为空数组）。 */
  refs: JDKRefInstance[]
  /** 引用数（= refs.length）。 */
  refCount: number
  /** 后端判定：现在删会不会被拒（仅 JDK 有；其它类型恒 false）。 */
  deleteBlocked: boolean
  /** 拒绝原因（direct / major / 空串）。 */
  deleteBlockedReason: string
}

/** 运行时类型展示名（专有名词，不进 i18n）。 */
export const RUNTIME_TYPE_LABEL: Record<string, string> = {
  jdk: 'JDK',
  nodejs: 'Node.js',
  python: 'Python',
}

/** 类型排序权重：JDK 在前（Java 服是主体），其次 Node，其余按名字。 */
const TYPE_ORDER: Record<string, number> = { jdk: 0, nodejs: 1, python: 2 }

/** 清单行筛选：全部 / 按类型。 */
export type RuntimeTypeFilter = 'all' | RuntimeRowType

/** 清单行来源筛选：全部 / 托管（平台装、删会清文件）/ 外部（用户自管、只删记录）。 */
export type RuntimeSourceFilter = 'all' | 'managed' | 'external'

/** JDK 侧输入（`?withRefs=1` 的形状）。 */
export interface JDKInventoryInput {
  id: number
  vendor: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  instances?: JDKRefInstance[]
  refCount?: number
  deleteBlocked?: boolean
  deleteBlockedReason?: string
}

/** 其它运行时侧输入（`GET /nodes/:id/runtimes` 的形状）。 */
export interface OtherRuntimeInput {
  id: number
  type: string
  name: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
}

/**
 * 合成清单行。
 *
 * 关键设计：**不做类型互斥过滤**。JDK 与 Node 走同一条渲染路径，
 * 类型只体现为一个列值与排序权重——这是消除「两个面板互斥」的根本做法。
 */
export function buildRuntimeInventory(
  jdks: JDKInventoryInput[],
  others: OtherRuntimeInput[],
): RuntimeInventoryRow[] {
  const rows: RuntimeInventoryRow[] = [
    ...jdks.map((j) => ({
      key: `jdk-${j.id}`,
      id: j.id,
      type: 'jdk' as const,
      name: j.vendor,
      majorVersion: j.majorVersion,
      version: j.version,
      arch: j.arch,
      path: j.path,
      managed: j.managed,
      refs: j.instances ?? [],
      refCount: j.refCount ?? j.instances?.length ?? 0,
      deleteBlocked: j.deleteBlocked ?? false,
      deleteBlockedReason: j.deleteBlockedReason ?? '',
    })),
    // 非 JDK 运行时原样进表；type=jdk 的行即使后端混进来也不重复呈现（JDK 侧已单独给全量引用信息）。
    ...others
      .filter((r) => r.type !== 'jdk')
      .map((r) => ({
        key: `${r.type}-${r.id}`,
        id: r.id,
        type: r.type,
        name: r.name,
        majorVersion: r.majorVersion,
        version: r.version,
        arch: r.arch,
        path: r.path,
        managed: r.managed,
        refs: [] as JDKRefInstance[],
        refCount: 0,
        deleteBlocked: false,
        deleteBlockedReason: '',
      })),
  ]

  // 排序：先类型，再大版本倒序（新版本在前），最后按名字——与 JDK 面板原本的
  // 「major_version desc, id desc」保持一致的阅读方向。
  return rows.sort((a, b) => {
    const ta = TYPE_ORDER[a.type] ?? 9
    const tb = TYPE_ORDER[b.type] ?? 9
    if (ta !== tb) return ta - tb
    if (a.majorVersion !== b.majorVersion) return b.majorVersion - a.majorVersion
    if (a.name !== b.name) return a.name.localeCompare(b.name)
    return a.id - b.id
  })
}

/** 按类型 + 来源 + 关键词筛选（纯函数，便于单测与本地即时反馈）。 */
export function filterRuntimeInventory(
  rows: RuntimeInventoryRow[],
  filters: { type: RuntimeTypeFilter; source: RuntimeSourceFilter; query: string },
): RuntimeInventoryRow[] {
  const q = filters.query.trim().toLowerCase()
  return rows.filter((r) => {
    if (filters.type !== 'all' && r.type !== filters.type) return false
    if (filters.source === 'managed' && !r.managed) return false
    if (filters.source === 'external' && r.managed) return false
    if (!q) return true
    // 搜索覆盖用户排障时会粘贴的每一列：厂商/登记名、版本、架构、路径。
    return `${r.name} ${r.majorVersion} ${r.version} ${r.arch} ${r.path}`.toLowerCase().includes(q)
  })
}

/** 类型计数（chips 上的数字；按来源筛选后的结果计数是不对的，所以按全量算）。 */
export function countByType(rows: RuntimeInventoryRow[]): Record<string, number> {
  const out: Record<string, number> = { all: rows.length }
  for (const r of rows) out[r.type] = (out[r.type] ?? 0) + 1
  return out
}

/** 引用关系的可读摘要：「5 直接 · 2 大版本」。 */
export function describeRefs(refs: JDKRefInstance[]): string {
  const direct = refs.filter((r) => r.binding === 'direct').length
  const major = refs.filter((r) => r.binding === 'major').length
  const parts: string[] = []
  if (direct > 0) parts.push(`${direct} 直接`)
  if (major > 0) parts.push(`${major} 大版本`)
  return parts.join(' · ')
}

/**
 * 删除前置判定的可读原因。
 *
 * 与后端 `deleteBlockedReason` 一一对应（direct / major），文案在这里而判定在服务端——
 * 前端**不重算**绑定关系，避免两处口径漂移。
 */
export function deleteBlockedText(row: RuntimeInventoryRow): string | null {
  if (!row.deleteBlocked) return null
  if (row.deleteBlockedReason === 'direct') return `被 ${row.refCount} 个实例直接绑定`
  if (row.deleteBlockedReason === 'major') return `被实例按 Java ${row.majorVersion} 大版本依赖`
  return `被 ${row.refCount} 个实例引用`
}
