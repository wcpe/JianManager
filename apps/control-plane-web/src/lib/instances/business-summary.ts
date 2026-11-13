import type { BusinessStatus } from '@/api/businessStatus'

/** 
 * 「业务插件」汇总列的展示形态。
 *
 * 抽成纯函数而不是写在 JSX 里，是因为**三种状态必须能各自被断言**，
 * 而混在一起最容易出的错就是把「没拿到数据」显示成「没有插件」。
 */
export type BusinessSummary =
  /** 无数据：列未开启 / 批量未返回 / 该台不在批里。显示 `--`，**不等于没装插件**。 */
  | { kind: 'unknown' }
  /** 探针明确上报了 0 个域。 */
  | { kind: 'none' }
  /** 有域：ok 个可用，total 个总数（degraded = total - ok）。 */
  | { kind: 'domains'; ok: number; total: number; degraded: number; stale: boolean }

/** 
 * 把域状态折算成展示形态。
 *
 * @param status 该实例的域状态；`undefined` 表示"还没数据"。
 */
export function summarizeBusiness(status: BusinessStatus | undefined): BusinessSummary {
  if (status === undefined) return { kind: 'unknown' }
  const domains = status.domains ?? []
  if (domains.length === 0) return { kind: 'none' }
  const total = domains.length
  const ok = domains.filter((d) => d.available).length
  return { kind: 'domains', ok, total, degraded: total - ok, stale: Boolean(status.stale) }
}

/** 
 * 业务域状态按值相等（供行组件 memo 比较器用）。
 *
 * 渲染这一格只依赖三样：域总数、可用域数、是否 stale。
 * 其余字段（manifestDigest、调用计数、最近调用时间）在这一格不显示，
 * 变化不该触发重渲染——否则每次轮询（计数会变）都会重画整行。
 *
 * 但**不能只比引用**：批量查询每次都返回新对象，引用永远不同，
 * 那样比较器等于失效，整表每 30 秒全量重渲染一次。
 */
export function sameBusinessStatus(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (!a || !b) return false
  const x = a as BusinessStatus
  const y = b as BusinessStatus
  const xd = x.domains ?? []
  const yd = y.domains ?? []
  if (xd.length !== yd.length) return false
  if (Boolean(x.stale) !== Boolean(y.stale)) return false
  const countOk = (list: readonly { available?: boolean }[]) =>
    list.filter((d) => d.available).length
  return countOk(xd) === countOk(yd)
}
