import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const root = path.resolve(__dirname, '../..')
// FR-283：组件博物馆迁 apps/ui-museum（原 web/wiki），经仓库根定位。
const repoRoot = path.resolve(root, '../..')
const museumRoot = path.join(repoRoot, 'apps/ui-museum')
const sourceRoot = path.join(root, 'src')

const firstWaveUi = [
  'badge',
  'button',
  'card',
  'checkbox',
  'context-menu-surface',
  'dialog',
  'dropdown-menu',
  'field-label',
  'gauge',
  'input',
  'label',
  'mini-bar',
  'panel',
  'password-input',
  'scrollable-dialog',
  'select',
  'sheet',
  'stat-card',
  'status-badge',
  'summary-chips',
  'table',
  'tabs',
  'textarea',
  'view-toggle',
]

const firstWaveCharts = [
  'RangePicker',
  'Sparkline',
  'TimeSeriesChart',
  'MonitorChart',
  'MonitorSkeleton',
  'MetricsOverviewStrip',
  // 全量对齐补录：它此前漏在清单外，于是 charts 目录里留了一份与包内逐字相同的副本
  // 而无人察觉（MonitoringPage 用的正是那份副本，包内实现反成死代码）。
  'MetricComparePanel',
]

const sharedHelpers = ['utils', 'threshold', 'brush', 'chart-hover', 'monitor-metrics']

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const full = path.join(dir, entry)
    const stat = statSync(full)
    if (stat.isDirectory()) return walk(full)
    return /\.(ts|tsx)$/.test(entry) ? [full] : []
  })
}

function rel(file: string): string {
  return path.relative(root, file).replaceAll(path.sep, '/')
}

describe('@jianmanager/ui package boundary', () => {
  it('exports the first-wave UI and helper modules from the package entry', async () => {
    const ui = await import('@jianmanager/ui')

    for (const exported of [
      'Button',
      'ContextMenuSurface',
      'Panel',
      'StatCard',
      'StatusBadge',
      'resourceLevel',
      'brushSelectionToWindow',
      'hoverSnapshotAt',
      'buildSnapshots',
      'cn',
    ]) {
      expect(ui, exported).toHaveProperty(exported)
    }
  }, 15_000)

  /**
   * 图表**组件**不得回到 barrel（2025-10 首屏走查）。
   *
   * barrel 被 120+ 个应用文件引用、几乎每个路由分块都静态可达；一旦它同步 re-export
   * 静态 import recharts 的图表模块（TimeSeriesChart / MonitorChart），recharts 的
   * 「被引用面」就与 react 同级，构建器把两者合并进同一个共享 chunk——实测即
   * `charts-*.js`（359 kB / gzip 约 106 kB），而**每个 chunk 都要 import 它拿 react**，
   * 于是它被写进 dist/index.html 的 modulepreload，任何页面（含落地页）首屏都得先下完。
   * 组件改走 `@jianmanager/ui/charts/*` 深路径后，recharts 只被真正画图的视图引用。
   *
   * 类型仍从 barrel 透出：类型导出编译期擦除、不产生运行时依赖，调用方无需改类型 import。
   */
  it('keeps chart components off the package entry, and reachable via deep paths', async () => {
    const ui = await import('@jianmanager/ui')
    for (const exported of firstWaveCharts) {
      expect(ui, `${exported} 应改走 @jianmanager/ui/charts/${exported} 深路径`).not.toHaveProperty(exported)
    }

    // 深路径仍可用（包 exports 映射 "./charts/*"），且落地页懒加载依赖它。
    const timeSeries = await import('@jianmanager/ui/charts/TimeSeriesChart')
    expect(timeSeries.TimeSeriesChart).toBeTypeOf('function')
    const rangePicker = await import('@jianmanager/ui/charts/RangePicker')
    expect(rangePicker.RangePicker).toBeTypeOf('function')
    expect(rangePicker.ResolutionPicker).toBeTypeOf('function')
  }, 15_000)

  it('no longer keeps design-system compat re-export layers in the app', () => {
    // 这三组路径曾是「旧入口 → 包」的再导出层。消费者改指包路径后它们已删除，
    // 此处断言其不复存在，防止再造出中转层——设计系统一律经 @jianmanager/ui 访问。
    const stale: string[] = []
    for (const name of firstWaveUi) {
      const file = path.join(sourceRoot, 'components/ui', `${name}.tsx`)
      if (existsSync(file)) stale.push(rel(file))
    }
    for (const name of firstWaveCharts) {
      const file = path.join(sourceRoot, 'components/charts', `${name}.tsx`)
      if (existsSync(file)) stale.push(rel(file))
    }
    for (const name of sharedHelpers) {
      const file = path.join(sourceRoot, 'lib', `${name}.ts`)
      if (existsSync(file)) stale.push(rel(file))
    }
    expect(stale, '设计系统一律经 @jianmanager/ui 访问，应用侧不应再有再导出层').toEqual([])
  })

  it('routes app consumers through @jianmanager/ui instead of legacy component paths', () => {
    // 原先还豁免 src/components/ui、src/components/charts、src/lib 三个目录——那是为放过
    // 再导出层自身。层已删除，豁免随之取消；设计系统的全部模块名一并在禁止清单内。
    const offenders = walk(sourceRoot)
      .filter((file) => !file.endsWith('.test.ts') && !file.endsWith('.dom.test.tsx'))
      .filter((file) => {
        const text = readFileSync(file, 'utf8')
        return /@\/components\/ui\/|@\/components\/charts\/(?:RangePicker|Sparkline|TimeSeriesChart|MonitorChart|MonitorSkeleton|MetricsOverviewStrip|MetricComparePanel)|@\/lib\/(?:utils|threshold|brush|chart-hover|monitor-metrics|theme|tone|stat-card|combobox|color-contrast|focus-ring|interaction-overlay)/.test(text)
      })
      .map(rel)

    expect(offenders).toEqual([])
  })

  it('creates a wiki project that imports the shared package instead of duplicating components', () => {
    const appPath = path.join(museumRoot, 'src/App.tsx')
    const packagePath = path.join(museumRoot, 'package.json')
    const vitePath = path.join(museumRoot, 'vite.config.ts')

    expect(existsSync(appPath)).toBe(true)
    expect(existsSync(packagePath)).toBe(true)
    expect(existsSync(vitePath)).toBe(true)

    const app = readFileSync(appPath, 'utf8')
    expect(app).toContain('@jianmanager/ui')
    for (const section of ['Foundation', 'Actions', 'Forms', 'Data', 'Overlay', 'Monitoring']) {
      expect(app).toContain(section)
    }
  })
})
