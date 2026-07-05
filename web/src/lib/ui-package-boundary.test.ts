import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const root = path.resolve(__dirname, '../..')
const sourceRoot = path.join(root, 'src')

const firstWaveUi = [
  'badge',
  'button',
  'card',
  'checkbox',
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
  })

  it('keeps first-wave legacy entries as package re-exports only', () => {
    for (const name of firstWaveUi) {
      const file = path.join(sourceRoot, 'components/ui', `${name}.tsx`)
      const text = readFileSync(file, 'utf8').trim()
      expect(text, rel(file)).toMatch(/from ['"]@jianmanager\/ui(?:\/[^'"]+)?['"]/)
    }

    for (const name of firstWaveCharts) {
      const file = path.join(sourceRoot, 'components/charts', `${name}.tsx`)
      const text = readFileSync(file, 'utf8').trim()
      expect(text, rel(file)).toMatch(/from ['"]@jianmanager\/ui(?:\/[^'"]+)?['"]/)
    }

    for (const name of sharedHelpers) {
      const file = path.join(sourceRoot, 'lib', `${name}.ts`)
      const text = readFileSync(file, 'utf8').trim()
      expect(text, rel(file)).toMatch(/from ['"]@jianmanager\/ui(?:\/[^'"]+)?['"]/)
    }
  })

  it('routes app consumers through @jianmanager/ui instead of legacy component paths', () => {
    const offenders = walk(sourceRoot)
      .filter((file) => !rel(file).startsWith('src/components/ui/'))
      .filter((file) => !rel(file).startsWith('src/components/charts/'))
      .filter((file) => !rel(file).startsWith('src/lib/'))
      .filter((file) => !file.endsWith('.test.ts') && !file.endsWith('.dom.test.tsx'))
      .filter((file) => {
        const text = readFileSync(file, 'utf8')
        return /@\/components\/ui\/|@\/components\/charts\/(?:RangePicker|Sparkline|TimeSeriesChart|MonitorChart|MonitorSkeleton|MetricsOverviewStrip)|@\/lib\/(?:utils|threshold|brush|chart-hover|monitor-metrics)/.test(text)
      })
      .map(rel)

    expect(offenders).toEqual([])
  })

  it('creates a wiki project that imports the shared package instead of duplicating components', () => {
    const appPath = path.join(root, 'wiki/src/App.tsx')
    const packagePath = path.join(root, 'wiki/package.json')
    const vitePath = path.join(root, 'wiki/vite.config.ts')

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
