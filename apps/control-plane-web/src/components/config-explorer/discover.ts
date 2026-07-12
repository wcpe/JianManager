/**
 * 已发现配置文件的展示整理（FR-071）。
 *
 * 后端 `GET /configs/discover` 返回扁平配置文件列表；前端按目录分组并稳定排序，
 * 供「已发现配置」面板呈现。纯函数，便于 vitest 覆盖。
 */

/** 与后端 DiscoveredConfig 对应。 */
export interface DiscoveredConfig {
  path: string
  format: string
  supported: boolean
}

/** 按目录分组的发现结果。 */
export interface DiscoverGroup {
  /** 目录相对路径（根为空串）。 */
  dir: string
  files: DiscoveredConfig[]
}

/** 取相对路径的父目录（根文件返回空串）。 */
export function dirOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? '' : path.slice(0, i)
}

/** 取文件名（basename）。 */
export function baseNameOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? path : path.slice(i + 1)
}

/**
 * 按目录分组并稳定排序：
 * - 组按目录名字典序（根目录 "" 永远排最前）；
 * - 组内文件按 basename 字典序。
 */
export function groupDiscovered(files: DiscoveredConfig[]): DiscoverGroup[] {
  const byDir = new Map<string, DiscoveredConfig[]>()
  for (const f of files) {
    const d = dirOf(f.path)
    const arr = byDir.get(d)
    if (arr) arr.push(f)
    else byDir.set(d, [f])
  }
  const dirs = [...byDir.keys()].sort((a, b) => {
    if (a === b) return 0
    if (a === '') return -1
    if (b === '') return 1
    return a < b ? -1 : 1
  })
  return dirs.map((dir) => ({
    dir,
    files: [...(byDir.get(dir) as DiscoveredConfig[])].sort((x, y) =>
      baseNameOf(x.path) < baseNameOf(y.path) ? -1 : baseNameOf(x.path) > baseNameOf(y.path) ? 1 : 0,
    ),
  }))
}
