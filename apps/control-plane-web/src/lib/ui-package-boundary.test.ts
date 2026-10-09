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
  it('exports the first-wave UI, chart and helper modules from the package entry', async () => {
    const ui = await import('@jianmanager/ui')

    for (const exported of [
      'Button',
      'ContextMenuSurface',
      'Panel',
      'StatCard',
      'StatusBadge',
      'RangePicker',
      'Sparkline',
      'TimeSeriesChart',
      'MonitorChart',
      'MonitorSkeleton',
      'MetricsOverviewStrip',
      'resourceLevel',
      'brushSelectionToWindow',
      'hoverSnapshotAt',
      'buildSnapshots',
      'cn',
    ]) {
      expect(ui, exported).toHaveProperty(exported)
    }
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
