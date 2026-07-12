import { describe, it, expect } from 'vitest'
import {
  formatBytes,
  formatterFor,
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
  type RawSeries,
} from './monitor-metrics'

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
    expect(ratioPctSeries([{ ts: 't', value: 10 }], [{ ts: 't', value: -1 }])[0].value).toBeNull()
  })
})

describe('定义表完整性', () => {
  it('节点 6 图 / 实例 6 图', () => {
    expect(NODE_CHART_DEFS).toHaveLength(6)
    expect(INSTANCE_CHART_DEFS).toHaveLength(6)
  })
  it('节点 6 图 id 与设计一致', () => {
    expect(NODE_CHART_DEFS.map((d) => d.id)).toEqual(['resource', 'load', 'cpu', 'memory', 'disk', 'network'])
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
    const out = buildChartSeries(NODE_CHART_DEFS[0], raw, id)
    expect(out.map((s) => s.key)).toEqual(['node_cpu_pct', 'mem_pct', 'disk_pct'])
    expect(out[1].points[0].value).toBe(50)
    expect(out[2].points[0].value).toBe(20)
  })

  it('内存图按 sources 取 used/total 两线', () => {
    const raw: RawSeries[] = [
      { metricKey: 'node_mem_used', points: [{ ts: 't0', value: 5 }] },
      { metricKey: 'node_mem_total', points: [{ ts: 't0', value: 9 }] },
    ]
    const out = buildChartSeries(NODE_CHART_DEFS[3], raw, id)
    expect(out.map((s) => s.key)).toEqual(['node_mem_used', 'node_mem_total'])
  })

  it('区块图按 world 拆线，忽略空 world', () => {
    const raw: RawSeries[] = [
      { metricKey: 'world_loaded_chunks', world: 'world', points: [{ ts: 't0', value: 100 }] },
      { metricKey: 'world_loaded_chunks', world: 'world_nether', points: [{ ts: 't0', value: 20 }] },
      { metricKey: 'world_loaded_chunks', world: '', points: [{ ts: 't0', value: 999 }] },
    ]
    const chunks = INSTANCE_CHART_DEFS.find((d) => d.id === 'chunks')!
    const out = buildChartSeries(chunks, raw, id)
    expect(out.map((s) => s.key)).toEqual(['world', 'world_nether'])
  })

  it('缺序列时跳过该线', () => {
    const out = buildChartSeries(INSTANCE_CHART_DEFS[0], [], id)
    expect(out).toEqual([])
  })

  it('worldFilter 非空时区块图只画该世界（FR-221 下钻）', () => {
    const raw: RawSeries[] = [
      { metricKey: 'world_loaded_chunks', world: 'world', points: [{ ts: 't0', value: 100 }] },
      { metricKey: 'world_loaded_chunks', world: 'world_nether', points: [{ ts: 't0', value: 20 }] },
    ]
    const chunks = INSTANCE_CHART_DEFS.find((d) => d.id === 'chunks')!
    const out = buildChartSeries(chunks, raw, id, 'world_nether')
    expect(out.map((s) => s.key)).toEqual(['world_nether'])
  })
})

// ===== FR-221 时序剖析增强 =====

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
})

describe('buildSnapshots', () => {
  const id = (k: string) => k
  it('按目录装配当前值 + 趋势点', () => {
    const raw: RawSeries[] = [
      { metricKey: 'node_cpu_pct', points: [{ ts: 't0', value: 40 }, { ts: 't1', value: 60 }] },
    ]
    const snaps = buildSnapshots(NODE_METRIC_CATALOG, raw)
    const cpu = snaps.find((s) => s.metricKey === 'node_cpu_pct')!
    expect(cpu.current).toBe(60)
    expect(cpu.points).toHaveLength(2)
    // 无数据的指标当前值为 null、点为空
    const load = snaps.find((s) => s.metricKey === 'node_load')!
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
    expect(out[0].name).toBe('monitor.metric.load1')
  })
  it('无匹配序列的 key 跳过', () => {
    const out = buildCompareSeries(['node_cpu_pct', 'node_net_rx_rate'], NODE_METRIC_CATALOG, raw, id)
    expect(out.map((s) => s.key)).toEqual(['node_cpu_pct'])
  })
  it('空选返回空', () => {
    expect(buildCompareSeries([], NODE_METRIC_CATALOG, raw, id)).toEqual([])
  })
})
