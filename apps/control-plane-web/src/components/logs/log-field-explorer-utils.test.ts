import { describe, expect, it } from 'vitest'
import {
  buildFieldFacetParams,
  buildFieldStatsParams,
  fieldValueExpression,
  fieldStatsValues,
  fieldExplorerStatus,
  isQueryableLogField,
} from './log-field-explorer-utils'

describe('受控日志字段查询', () => {
  it('字段名遵守 Worker validField，不允许表达式或管道成为字段名', () => {
    expect(isQueryableLogField('duration_ms')).toBe(true)
    expect(isQueryableLogField('_time')).toBe(true)
    for (const field of ['', '5field', 'duration.ms', 'level OR *', 'level|stats', 'level"']) {
      expect(isQueryableLogField(field)).toBe(false)
      expect(() => fieldValueExpression(field, 'x')).toThrow()
    }
  })

  it('引号、反斜杠、换行及 OR 始终保留在精确字符串值内', () => {
    const value = 'a" OR log_source_id:* \\x\nnext'
    const literal = JSON.stringify(value)
    expect(fieldValueExpression('shard', value)).toBe(`(shard:=${literal})`)
    expect(fieldValueExpression('shard', value, true)).toBe(`NOT (shard:=${literal})`)
    expect(fieldValueExpression('shard', '')).toBe('(shard:="")')
  })

  it('字段分布和指标查询克隆参数且保留同 view/scope/filter/time 身份', () => {
    const scope = Object.freeze({ viewId: 'v-1', instanceId: 2, from: 'start', to: 'end', keyword: '(a OR b)', onlineOnly: true, groupBy: 'old', timeBucket: '6h', metricField: 'old_metric' })
    expect(buildFieldFacetParams(scope, 'shard')).toMatchObject({ ...scope, dimensions: 'shard', dimensionLimit: 20 })
    expect(buildFieldStatsParams(scope, 'duration_ms', 'shard', '5m')).toEqual({ ...scope, metricField: 'duration_ms', groupBy: 'shard', timeBucket: '5m' })
    expect(scope.metricField).toBe('old_metric')
    expect(() => buildFieldStatsParams(scope, 'x | stats', '', '')).toThrow()
    expect(() => buildFieldStatsParams(scope, 'x', 'bad OR *', '')).toThrow()
    expect(() => buildFieldStatsParams(scope, 'x', '', '1m|stats')).toThrow()
  })
})

describe('字段探索结果的可归因性', () => {
  it('失败、unsupported、partial、truncated 均不会被解释为无数据成功', () => {
    expect(fieldExplorerStatus({}, new Error('network')).issues).toContain('failed')
    expect(fieldExplorerStatus({ coverage: { complete: true, targets: [{ reasons: ['LOG_UNSUPPORTED'] }] } }).issues).toContain('unsupported')
    const issues = fieldExplorerStatus({ coverage: { complete: false }, truncated: true }).issues
    expect(issues).toContain('partial')
    expect(issues).toContain('truncated')
    expect(fieldExplorerStatus(undefined).issues).toContain('unverified')
  })

  it('精确结果要求自身覆盖和质量均精确，不借其他查询背书', () => {
    expect(fieldExplorerStatus({ coverage: { complete: true }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' } }).issues).toEqual([])
    expect(fieldExplorerStatus({ coverage: { complete: true } }).issues).toContain('unverified')
    expect(fieldExplorerStatus({ coverage: { complete: false, pending: true } }).issues).toContain('pending')
    expect(fieldExplorerStatus({ coverage: { complete: true }, quality: { duplicate_quality: 'exact', stats_quality: 'partial' } }).issues).toContain('approximate')
  })

  it('数值统计不把缺失/NaN/空值变成 0，无样本时均值不可用', () => {
    expect(fieldStatsValues({ agg: { count: 0, sum: 0, min: 0, max: 0, has_min_max: false } })).toEqual({ count: 0, sum: undefined, min: undefined, max: undefined, avg: undefined })
    expect(fieldStatsValues({ agg: { count: 2, sum: 10, min: 3, max: 7, has_min_max: true } })).toEqual({ count: 2, sum: 10, min: 3, max: 7, avg: 5 })
    expect(fieldStatsValues({ agg: { count: 2, sum: Number.NaN, min: null, max: undefined } }).avg).toBeUndefined()
  })
})
