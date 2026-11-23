import { describe, it, expect } from 'vitest'
import {
  formatBytes,
  formatterFor,
  compactFormatter,
  ratioPctSeries,
  buildChartSeries,
  NODE_CHART_DEFS,
  INSTANCE_CHART_DEFS,
  PLATFORM_CHART_DEFS,
  catalogFor,
  latestValue,
  buildSnapshots,
  buildCompareSeries,
  NODE_METRIC_CATALOG,
  type MetricChartDef,
  type MetricSnapshot,
  type RawSeries,
} from './monitor-metrics'

/** 
 * 按 id 取图表定义。
 * 定义表里没有该 id 时直接抛错，而不是让 undefined 一路传进断言——
 * 否则「图定义被改名或删除」会表现成「曲线断言莫名其妙失败」，掩盖真正原因。
 */
function defById(defs: MetricChartDef[], id: string): MetricChartDef {
  const def = defs.find((d) => d.id === id)
  if (!def) throw new Error(`图表定义缺失：${id}`)
  return def
}

/** 按 metricKey 取快照（同上：取不到即抛，避免 undefined 混进断言）。 */
function snapshotByKey(snaps: MetricSnapshot[], metricKey: string): MetricSnapshot {
  const snap = snaps.find((s) => s.metricKey === metricKey)
  if (!snap) throw new Error(`快照缺失：${metricKey}`)
  return snap
}

describe('formatBytes', () => {
  it('按量级取 G/M/K', () => {
    expect(formatBytes(2e9)).toBe('1.9G')
    expect(formatBytes(5e6)).toBe('5M')
    expect(formatBytes(2048)).toBe('2K')
  })
  it('非正数归 0', () => {
    expect(formatBytes(0)).toBe('0')
    expect(formatBytes(-1)).toBe('0')
    expect(formatBytes(NaN)).toBe('0')
  })
})

describe('formatterFor', () => {
  it('pct/load/ms/tps/count 各自格式', () => {
    expect(formatterFor('pct')(42.6)).toBe('43%')
    expect(formatterFor('load')(0.426)).toBe('0.43')
    expect(formatterFor('ms')(12.34)).toBe('12.3ms')
    expect(formatterFor('tps')(19.96)).toBe('20.0')
    expect(formatterFor('count')(7.8)).toBe('8')
  })
  it('bytesPerSec 带 /s 后缀', () => {
    expect(formatterFor('bytesPerSec')(5e6)).toBe('5M/s')
  })
})

describe('compactFormatter', () => {
  it('按量级取 G/M/K，保留 1 位小数、整数省 .0', () => {
    expect(compactFormatter(17179869184)).toBe('17.2G')
    expect(compactFormatter(5e6)).toBe('5M')
    expect(compactFormatter(1500)).toBe('1.5K')
  })
  it('小量级原值输出（1 位小数、整数省 .0）', () => {
    expect(compactFormatter(42)).toBe('42')
    expect(compactFormatter(1.26)).toBe('1.3')
    expect(compactFormatter(0)).toBe('0')
  })
  it('负数保留符号，非有限数归 0', () => {
    expect(compactFormatter(-2500)).toBe('-2.5K')
    expect(compactFormatter(NaN)).toBe('0')
  })
})

describe('ratioPctSeries', () => {
  const num = [
    { ts: 't0', value: 50 },
    { ts: 't1', value: 80 },
    { ts: 't2', value: null },
  ]
  const den = [
    { ts: 't0', value: 100 },
    { ts: 't1', value: 0 },
    { ts: 't2', value: 100 },
  ]
  it('逐点算占比', () => {
    expect(ratioPctSeries(num, den).map((p) => p.value)).toEqual([50, null, null])
  })
  it('分母≤0 或缺测处为 null', () => {
    // 整条取值序列比对：既钉住占比为 null，也钉住「只有这一个点」（避免为空时假绿）
    expect(ratioPctSeries([{ ts: 't', value: 10 }], [{ ts: 't', value: -1 }]).map((p) => p.value)).toEqual([null])
  })
})

describe('定义表完整性', () => {
  it('节点 10 图（含共享 Bot Worker）/ 实例 9 图（含三张业务桥图）', () => {
    expect(NODE_CHART_DEFS).toHaveLength(10)
    // 6 张原有 + 3 张桥中转：桥堵了事件发不出去，但实例页看起来一切健康，
    // 所以这三张必须与 TPS/内存并列放在实例页。
    expect(INSTANCE_CHART_DEFS).toHaveLength(9)
    expect(INSTANCE_CHART_DEFS.map((d) => d.id)).toContain('bridge-queue')
    expect(INSTANCE_CHART_DEFS.map((d) => d.id)).toContain('bridge-rejected')
    expect(INSTANCE_CHART_DEFS.map((d) => d.id)).toContain('bridge-health')
  })
  it('节点图含共享 Bot Worker 历史指标', () => {
    expect(NODE_CHART_DEFS.map((d) => d.id)).toEqual([
      'resource', 'load', 'cpu', 'memory', 'disk', 'network',
      'bot-runtime-memory', 'bot-runtime-cpu', 'bot-runtime-bots', 'bot-runtime-event-loop',
    ])
  })
  it('平台 def 只含 overview 聚合可得的 4 指标', () => {
    expect(PLATFORM_CHART_DEFS.map((d) => d.id)).toEqual(['cpu', 'load', 'memory', 'players'])
    // 平台图均为单 metricKey（无派生占比，避免引用 overview 没有的 total 序列）
    for (const d of PLATFORM_CHART_DEFS) {
      expect(d.sources).toHaveLength(1)
      expect(d.derive).toBeUndefined()
    }
  })
})

describe('buildChartSeries', () => {
  const id = (k: string) => k // 注入恒等翻译以校验结构

  it('资源使用率图：CPU% + 内存% + 磁盘% 三线', () => {
    const raw: RawSeries[] = [
      { metricKey: 'node_cpu_pct', points: [{ ts: 't0', value: 40 }] },
      { metricKey: 'node_mem_used', points: [{ ts: 't0', value: 50 }] },
      { metricKey: 'node_mem_total', points: [{ ts: 't0', value: 100 }] },
      { metricKey: 'node_disk_used', points: [{ ts: 't0', value: 20 }] },
      { metricKey: 'node_disk_total', points: [{ ts: 't0', value: 100 }] },
    ]
    const out = buildChartSeries(defById(NODE_CHART_DEFS, 'resource'), raw, id)
    expect(out.map((s) => s.key)).toEqual(['node_cpu_pct', 'mem_pct', 'disk_pct'])
    // 三条线各自的点值：CPU 为源序列原值，内存/磁盘由 used/total 派生为百分比
    // （整表比对而非逐条取下标：顺带钉住「每条线各一个点」）
    expect(out.map((s) => s.points.map((p) => p.value))).toEqual([[40], [50], [20]])
  })

  it('内存图按 sources 取 used/total 两线', () => {
    const raw: RawSeries[] = [
      { metricKey: 'node_mem_used', points: [{ ts: 't0', value: 5 }] },
      { metricKey: 'node_mem_total', points: [{ ts: 't0', value: 9 }] },
    ]
    const out = buildChartSeries(defById(NODE_CHART_DEFS, 'memory'), raw, id)
    expect(out.map((s) => s.key)).toEqual(['node_mem_used', 'node_mem_total'])
  })

  it('区块图按 world 拆线，忽略空 world', () => {
    const raw: RawSeries[] = [
      { metricKey: 'world_loaded_chunks', world: 'world', points: [{ ts: 't0', value: 100 }] },
      { metricKey: 'world_loaded_chunks', world: 'world_nether', points: [{ ts: 't0', value: 20 }] },
      { metricKey: 'world_loaded_chunks', world: '', points: [{ ts: 't0', value: 999 }] },
    ]
    const chunks = defById(INSTANCE_CHART_DEFS, 'chunks')
    const out = buildChartSeries(chunks, raw, id)
    expect(out.map((s) => s.key)).toEqual(['world', 'world_nether'])
  })

  it('缺序列时跳过该线', () => {
    const out = buildChartSeries(defById(INSTANCE_CHART_DEFS, 'tps'), [], id)
    expect(out).toEqual([])
  })

  it('worldFilter 非空时区块图只画该世界（下钻）', () => {
    const raw: RawSeries[] = [
      { metricKey: 'world_loaded_chunks', world: 'world', points: [{ ts: 't0', value: 100 }] },
      { metricKey: 'world_loaded_chunks', world: 'world_nether', points: [{ ts: 't0', value: 20 }] },
    ]
    const chunks = defById(INSTANCE_CHART_DEFS, 'chunks')
    const out = buildChartSeries(chunks, raw, id, 'world_nether')
    expect(out.map((s) => s.key)).toEqual(['world_nether'])
  })

  it('资源图：没有 total 序列时回退到已落库的百分比指标，而不是静默丢掉内存/磁盘线', () => {
    const raw: RawSeries[] = [
      { metricKey: 'node_cpu_pct', points: [{ ts: 't0', value: 40 }] },
      { metricKey: 'node_mem_used', points: [{ ts: 't0', value: 5 }] },
      { metricKey: 'node_mem_pct', points: [{ ts: 't0', value: 50 }] },
      { metricKey: 'node_disk_used', points: [{ ts: 't0', value: 2 }] },
      { metricKey: 'node_disk_pct', points: [{ ts: 't0', value: 20 }] },
    ]
    const out = buildChartSeries(defById(NODE_CHART_DEFS, 'resource'), raw, id)
    expect(out.map((s) => s.key)).toEqual(['node_cpu_pct', 'mem_pct', 'disk_pct'])
    expect(out.map((s) => s.points.map((p) => p.value))).toEqual([[40], [50], [20]])
  })

  it('实例迁移后同一指标多条序列：合并时间线，不因枚举顺序丢新数据', () => {
    const raw: RawSeries[] = [
      { metricKey: 'inst_tps', points: [{ ts: '2026-01-01T00:00:00Z', value: 9 }] },
      { metricKey: 'inst_tps', points: [{ ts: '2026-01-01T01:00:00Z', value: 20 }] },
    ]
    const out = buildChartSeries(defById(INSTANCE_CHART_DEFS, 'tps'), raw, id)
    expect(out).toHaveLength(1)
    expect(out[0]?.points).toEqual([
      { ts: '2026-01-01T00:00:00Z', value: 9 },
      { ts: '2026-01-01T01:00:00Z', value: 20 },
    ])
    // 旧序列在本窗口内完全无点、新序列有数据时，不能显示为空
    const migrated: RawSeries[] = [
      { metricKey: 'inst_tps', points: [] },
      { metricKey: 'inst_tps', points: [{ ts: '2026-01-01T01:00:00Z', value: 20 }] },
    ]
    expect(buildChartSeries(defById(INSTANCE_CHART_DEFS, 'tps'), migrated, id)[0]?.points).toEqual([
      { ts: '2026-01-01T01:00:00Z', value: 20 },
    ])
  })

  it('分世界图：同一 world 的多条序列（迁移）合并为一根线，不产生重复 key', () => {
    const raw: RawSeries[] = [
      { metricKey: 'world_loaded_chunks', world: 'world', points: [{ ts: 't0', value: 100 }] },
      { metricKey: 'world_loaded_chunks', world: 'world', points: [{ ts: 't1', value: 120 }] },
    ]
    const out = buildChartSeries(defById(INSTANCE_CHART_DEFS, 'chunks'), raw, id)
    expect(out).toHaveLength(1)
    expect(out[0]?.key).toBe('world')
    expect(out[0]?.points).toHaveLength(2)
  })
})

// =====时序剖析增强 =====

describe('catalogFor', () => {
  it('据 target 类型取指标目录', () => {
    expect(catalogFor('node')).toBe(NODE_METRIC_CATALOG)
    expect(catalogFor('platform').map((c) => c.metricKey)).toContain('inst_players_online')
    expect(catalogFor('instance').map((c) => c.metricKey)).toContain('inst_tps')
  })
})

describe('latestValue', () => {
  const raw: RawSeries[] = [
    { metricKey: 'node_cpu_pct', points: [{ ts: 't0', value: 40 }, { ts: 't1', value: 55 }, { ts: 't2', value: null }] },
  ]
  it('取最后一个非空值（跳过末尾缺测）', () => {
    expect(latestValue(raw, 'node_cpu_pct')).toBe(55)
  })
  it('无序列/全缺测返回 null', () => {
    expect(latestValue(raw, 'node_load')).toBeNull()
    expect(latestValue([{ metricKey: 'node_load', points: [{ ts: 't0', value: null }] }], 'node_load')).toBeNull()
  })
  it('同指标多序列（实例迁移）取全局最新时点的值，与序列顺序无关', () => {
    const migrated: RawSeries[] = [
      { metricKey: 'inst_tps', points: [{ ts: '2026-01-01T00:00:00Z', value: 9 }] },
      { metricKey: 'inst_tps', points: [{ ts: '2026-01-01T01:00:00Z', value: 20 }] },
    ]
    expect(latestValue(migrated, 'inst_tps')).toBe(20)
    expect(latestValue([...migrated].reverse(), 'inst_tps')).toBe(20)
  })
})

describe('buildSnapshots', () => {
  const id = (k: string) => k
  it('按目录装配当前值 + 趋势点', () => {
    const raw: RawSeries[] = [
      { metricKey: 'node_cpu_pct', points: [{ ts: 't0', value: 40 }, { ts: 't1', value: 60 }] },
    ]
    const snaps = buildSnapshots(NODE_METRIC_CATALOG, raw)
    const cpu = snapshotByKey(snaps, 'node_cpu_pct')
    expect(cpu.current).toBe(60)
    expect(cpu.points).toHaveLength(2)
    // 无数据的指标当前值为 null、点为空
    const load = snapshotByKey(snaps, 'node_load')
    expect(load.current).toBeNull()
    expect(load.points).toEqual([])
    // 目录全量覆盖
    expect(snaps).toHaveLength(NODE_METRIC_CATALOG.length)
    void id
  })
})

describe('buildCompareSeries', () => {
  const id = (k: string) => k
  const raw: RawSeries[] = [
    { metricKey: 'node_cpu_pct', points: [{ ts: 't0', value: 40 }] },
    { metricKey: 'node_load', points: [{ ts: 't0', value: 1.2 }] },
  ]
  it('按选中顺序叠加多条序列', () => {
    const out = buildCompareSeries(['node_load', 'node_cpu_pct'], NODE_METRIC_CATALOG, raw, id)
    expect(out.map((s) => s.key)).toEqual(['node_load', 'node_cpu_pct'])
    // 名称同样按选中顺序取目录 nameKey（整表比对，顺带钉住条数）
    expect(out.map((s) => s.name)).toEqual(['monitor.metric.load1', 'monitor.metric.cpu'])
  })
  it('每条序列带出目录 format（供 Y 轴/hover 选格式器）', () => {
    const out = buildCompareSeries(['node_load', 'node_cpu_pct'], NODE_METRIC_CATALOG, raw, id)
    expect(out.map((s) => s.format)).toEqual(['load', 'pct'])
  })
  it('无匹配序列的 key 跳过', () => {
    const out = buildCompareSeries(['node_cpu_pct', 'node_net_rx_rate'], NODE_METRIC_CATALOG, raw, id)
    expect(out.map((s) => s.key)).toEqual(['node_cpu_pct'])
  })
  it('空选返回空', () => {
    expect(buildCompareSeries([], NODE_METRIC_CATALOG, raw, id)).toEqual([])
  })
})

// 重叠序列不能让 SQL 枚举顺序决定同刻值；迁移后的活跃序列优先。
describe('monitor migration overlap regression', () => {
  const ts = (minute: number) => `2026-01-01T00:0${minute}:00Z`
  it.each([
    ['inst_tps', 'tps', undefined],
    ['world_loaded_chunks', 'chunks', 'world'],
  ] as const)('%s 重叠时取最新有效序列，保留并集且不被旧 null 覆盖', (metricKey, chart, world) => {
    const old: RawSeries = { metricKey, world, points: [
      { ts: ts(0), value: 7 }, { ts: ts(1), value: 9 },
      { ts: ts(2), value: null }, { ts: ts(4), value: null },
    ] }
    const active: RawSeries = { metricKey, world, points: [
      { ts: ts(1), value: 20 }, { ts: ts(2), value: 21 }, { ts: ts(3), value: 22 },
    ] }
    const expected = [
      { ts: ts(0), value: 7 }, { ts: ts(1), value: 20 },
      { ts: ts(2), value: 21 }, { ts: ts(3), value: 22 }, { ts: ts(4), value: null },
    ]
    for (const raw of [[old, active], [active, old]]) {
      expect(buildChartSeries(defById(INSTANCE_CHART_DEFS, chart), raw, (k) => k))
        .toEqual([{ key: world ?? metricKey, name: world ?? 'monitor.metric.tps', points: expected }])
    }
  })

  it.each([['inst_tps', 'tps', undefined], ['world_loaded_chunks', 'chunks', 'world']] as const)(
    '%s 同样活跃时按完整点时间/数值确定性取舍，与序列和点的枚举顺序无关',
    (metricKey, chart, world) => {
      const a: RawSeries = { metricKey, world, points: [{ ts: ts(1), value: 9 }, { ts: ts(2), value: 20 }] }
      const b: RawSeries = { metricKey, world, points: [{ ts: ts(1), value: 10 }, { ts: ts(2), value: 20 }] }
      const expected = [{ ts: ts(1), value: 10 }, { ts: ts(2), value: 20 }]
      for (const raw of [[a, b], [b, a], [a, { ...b, points: [...b.points].reverse() }]]) {
        expect(buildChartSeries(defById(INSTANCE_CHART_DEFS, chart), raw, (k) => k)[0]?.points).toEqual(expected)
      }
    },
  )

  it('混合历史逐点回退百分比，取时间并集，真实0不被回退替换且不反算total', () => {
    const raw: RawSeries[] = ['mem', 'disk'].flatMap((resource) => [
      { metricKey: `node_${resource}_used`, points: [
        { ts: ts(0), value: 10 }, { ts: ts(1), value: 20 },
        { ts: ts(2), value: 0 }, { ts: ts(3), value: null }, { ts: ts(4), value: 10 },
      ] },
      { metricKey: `node_${resource}_total`, points: [
        { ts: ts(0), value: 100 }, { ts: ts(1), value: null },
        { ts: ts(2), value: 100 }, { ts: ts(3), value: 100 }, { ts: ts(4), value: 0 },
      ] },
      { metricKey: `node_${resource}_pct`, points: [
        { ts: ts(0), value: 99 }, { ts: ts(1), value: 25 },
        { ts: ts(2), value: 77 }, { ts: ts(3), value: null },
        { ts: ts(4), value: 0 }, { ts: ts(5), value: 35 },
      ] },
    ])
    const expected = [
      { ts: ts(0), value: 10 }, { ts: ts(1), value: 25 }, { ts: ts(2), value: 0 },
      { ts: ts(3), value: null }, { ts: ts(4), value: 0 }, { ts: ts(5), value: 35 },
    ]
    expect(buildChartSeries(defById(NODE_CHART_DEFS, 'resource'), raw, (k) => k).map((s) => s.points))
      .toEqual([expected, expected])
    expect(buildChartSeries(defById(NODE_CHART_DEFS, 'memory'), raw, (k) => k)[1]?.points)
      .toEqual(raw.find((s) => s.metricKey === 'node_mem_total')?.points)
  })
})
