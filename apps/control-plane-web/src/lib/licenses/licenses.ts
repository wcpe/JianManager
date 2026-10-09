/**
 * 开源许可页纯逻辑（FR-135）：依赖唯一键、按包名过滤、运行时/开发分区计数。
 * 从 `LicensesPage` 下沉以便单测，组件只负责渲染。
 */

/** 分区/过滤所需的最小依赖结构（`api/licenses.LicenseDependency` 的子集）。 */
export interface DepLike {
  name: string
  version: string
  /** 来源：web/bot-worker 为 npm，go 为 go.mod。 */
  scope: string
  /** 运行时依赖 vs 开发依赖。 */
  type: 'runtime' | 'dev'
  license: string
}

/** 依赖唯一键：同名包可能跨 web/bot-worker/go 多源出现，故以 scope|name|version 区分。 */
export function depKey(d: DepLike): string {
  return `${d.scope}|${d.name}|${d.version}`
}

/**
 * 按包名子串过滤（大小写不敏感）。
 * 空查询返回原数组同一引用（避免无谓复制，便于 React 引用比较）。
 */
export function filterByName<T extends Pick<DepLike, 'name'>>(deps: T[], query: string): T[] {
  const q = query.trim().toLowerCase()
  if (!q) return deps
  return deps.filter((d) => d.name.toLowerCase().includes(q))
}

/** 运行时/开发分区结果 + 汇总计数（喂给 StatCard）。 */
export interface DepPartition<T> {
  runtime: T[]
  dev: T[]
  runtimeCount: number
  devCount: number
  total: number
  /** 去重后的非空许可证种类数。 */
  licenseCount: number
}

/** 按 type 拆分运行时/开发两区，并统计总数与许可证种类数。 */
export function partitionDeps<T extends Pick<DepLike, 'type' | 'license'>>(
  deps: T[],
): DepPartition<T> {
  const runtime = deps.filter((d) => d.type === 'runtime')
  const dev = deps.filter((d) => d.type === 'dev')
  const licenses = new Set<string>()
  for (const d of deps) {
    if (d.license) licenses.add(d.license)
  }
  return {
    runtime,
    dev,
    runtimeCount: runtime.length,
    devCount: dev.length,
    total: deps.length,
    licenseCount: licenses.size,
  }
}
