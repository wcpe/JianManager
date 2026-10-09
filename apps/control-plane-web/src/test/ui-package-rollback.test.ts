import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * 业务视图/lib 回迁后的包边界断言（ADR-097 撤销）。
 *
 * 背景：ADR-097 曾把全部业务视图与业务契约并入 `@jianmanager/ui`，使组件库反向承担了
 * 业务职责——包内出现 instance / console / client-dist 等域概念，应用侧的「接线层」被
 * 抽成只剩取数。该方向已撤销：业务视图与业务 lib 全部回迁应用侧，包回归设计系统。
 *
 * 本测试把该边界固化下来，防止回流。与既有的 `ui-package-boundary.test.ts` 互补：
 * 那个文件守的是「第一波设计系统模块必须经包访问」，本文件守的是「包内不得再有业务」。
 */

const root = path.resolve(__dirname, '../..')          // apps/control-plane-web
const repoRoot = path.resolve(root, '../..')
const pkgRoot = path.join(repoRoot, 'packages/ui')
const pkgSrc = path.join(pkgRoot, 'src')
const appViews = path.join(root, 'src/components/views')

/** 包内允许保留的 lib 模块——设计系统的依赖闭包（含被原语与博物馆直接使用者）。 */
const DESIGN_SYSTEM_LIBS = new Set([
  'brush', 'chart-hover', 'color-contrast', 'combobox', 'focus-ring',
  'interaction-overlay', 'monitor-metrics', 'stat-card', 'theme', 'threshold',
  'tone', 'utils',
])

function walk(dir: string): string[] {
  if (!existsSync(dir)) return []
  return readdirSync(dir).flatMap((entry) => {
    const full = path.join(dir, entry)
    return statSync(full).isDirectory() ? walk(full) : [full]
  })
}

const pkgSources = walk(pkgSrc).filter((f) => /\.(ts|tsx)$/.test(f))

describe('业务视图/lib 回迁后的包边界', () => {
  it('包内不再有业务视图目录', () => {
    expect(existsSync(path.join(pkgSrc, 'components/views'))).toBe(false)
  })

  it('包内不导入应用侧代码（无 @/ 别名，也不逃出包目录）', () => {
    const offenders: string[] = []
    for (const file of pkgSources) {
      const text = readFileSync(file, 'utf8')
      if (/from '@\//.test(text)) offenders.push(`${path.relative(repoRoot, file)}: @/ 别名`)
      // 逃出包的相对导入：packages/ui/src/... 里出现三层以上 ../
      for (const m of text.matchAll(/from '((?:\.\.\/){3,}[^']*)'/g)) {
        offenders.push(`${path.relative(repoRoot, file)}: ${m[1]}`)
      }
    }
    expect(offenders).toEqual([])
  })

  it('包内 lib 只剩设计系统模块', () => {
    const libDir = path.join(pkgSrc, 'lib')
    const modules = readdirSync(libDir)
      .filter((f) => /\.(ts|tsx)$/.test(f) && !f.includes('.test.'))
      .map((f) => f.replace(/\.(ts|tsx)$/, ''))
    const business = modules.filter((m) => !DESIGN_SYSTEM_LIBS.has(m))
    expect(business, '包内不应再有业务 lib').toEqual([])
  })

  it('包入口不导出业务视图，也不导出业务 lib', () => {
    const barrel = readFileSync(path.join(pkgSrc, 'index.ts'), 'utf8')
    expect(barrel, '不应再导出 components/views').not.toMatch(/from '\.\/components\/views\//)

    const exportedLibs = [...barrel.matchAll(/from '\.\/lib\/([^']+)'/g)].map((m) => m[1])
    const business = exportedLibs.filter((m) => !DESIGN_SYSTEM_LIBS.has(m.split('/')[0]))
    expect(business, '包入口不应再导出业务 lib').toEqual([])
  })

  it('包依赖只剩设计系统所需', () => {
    const pkg = JSON.parse(readFileSync(path.join(pkgRoot, 'package.json'), 'utf8'))
    const deps = Object.keys(pkg.dependencies ?? {})
    // 业务视图带进来的依赖应已移除；这些包现由应用自行声明
    const businessOnly = deps.filter((d) =>
      ['fflate', 'react-qr-code', 'react-grid-layout', 'react-markdown', 'remark-gfm']
        .includes(d) || d.startsWith('@codemirror/'))
    expect(businessOnly, '包依赖不应残留业务视图专用项').toEqual([])
  })

  it('应用侧视图目录承载全部回迁的域', () => {
    expect(existsSync(appViews)).toBe(true)
    const domains = readdirSync(appViews)
    // 抽样：各规模档位各取一个，确认回迁落点正确
    for (const d of ['instances', 'console', 'client-dist', 'nodes', 'explorer', 'licenses']) {
      expect(domains, `缺少域 ${d}`).toContain(d)
    }
  })

  /**
   * 目录组织不变量（回迁后的整理成果）。
   *
   * 回迁把 294 个 lib 文件与 52 个 components 文件一次性倒进两层平铺，找文件靠文件名猜域。
   * 现按域归档：lib 对齐 views 的域，跨域工具进 shared/，use-* 进 hooks/。
   * 这里守住「根层只放目录」——平铺一旦回来，域边界也就没了。
   */
  const looseFilesIn = (dir: string) =>
    readdirSync(dir).filter((e) => statSync(path.join(dir, e)).isFile())

  it('应用侧 lib 按域分组，根层不再平铺', () => {
    const libDir = path.join(root, 'src/lib')
    expect(looseFilesIn(libDir), 'lib 根层不应再有平铺文件').toEqual([])

    const dirs = readdirSync(libDir)
    for (const d of ['instances', 'console', 'client-dist', 'shared', 'hooks']) {
      expect(dirs, `缺少 lib 域 ${d}`).toContain(d)
    }
  })

  it('应用侧 components 按域分组，根层不再平铺', () => {
    const compDir = path.join(root, 'src/components')
    expect(looseFilesIn(compDir), 'components 根层不应再有平铺文件').toEqual([])
  })

  it('应用侧 views 按域分组，根层不再平铺', () => {
    expect(existsSync(appViews)).toBe(true)
    expect(looseFilesIn(appViews), 'views 根层不应再有平铺文件').toEqual([])
  })
})
