import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest'
import { server } from '@jianmanager/devmock/server'
import { db, resetDb } from '@jianmanager/devmock/db'
import type { CapacityForecastInfo, SLOInfo } from '@jianmanager/devmock/contracts'

/**
 * 容量预测 / 可用性（FR-463/464）**经 mock handler** 的契约测试。
 *
 * 存在的理由：旧用例用手写响应体（`server.use(http.get(...) => HttpResponse.json({...}))`）
 * 覆盖「无可用证据」等负例，**完全绕过 mock handler** —— 即便 mock 的字段名/分档逻辑全错，
 * 那些用例仍然绿，「mock 与真后端契约漂移」零守护（自审 M7）。
 * 本文件一律打真 mock 路由，只断言 mock 自身产出的响应。
 */

const BASE = 'http://localhost/api/v1'

async function login(): Promise<string> {
  const r = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin123' }),
  })
  expect(r.status).toBe(200)
  return ((await r.json()) as { accessToken: string }).accessToken
}

async function fetchSLO(token: string, query: string): Promise<SLOInfo> {
  const r = await fetch(`${BASE}/metrics/slo${query}`, { headers: { Authorization: `Bearer ${token}` } })
  expect(r.status).toBe(200)
  return (await r.json()) as SLOInfo
}

async function fetchCapacity(token: string, query: string): Promise<CapacityForecastInfo> {
  const r = await fetch(`${BASE}/metrics/capacity/forecast${query}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(r.status).toBe(200)
  return (await r.json()) as CapacityForecastInfo
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
})
afterAll(() => server.close())

describe('可用性 mock（FR-463）：applicable 两态都可复现（M7）', () => {
  it('有运行中实例 → platform 维度 applicable=true 且给可用率/预算数字', async () => {
    const token = await login()
    const body = await fetchSLO(token, '?scope=platform&range=24h')
    expect(body.applicable).toBe(true)
    expect(body.totalSamples).toBeGreaterThan(0)
    expect(body.upSamples).toBeGreaterThan(0)
    expect(body.budgetAllowedSec).toBeGreaterThan(0)
    expect(body.availability).toBeGreaterThan(0)
  })

  it('无任何运行中实例 → platform 维度 applicable=false 且可用率/预算全 0', async () => {
    // 按 seed 数据构造「无可用证据」：探针只在实例运行时上报 inst_uptime（slo.go:72），
    // 故把全部实例置为非 RUNNING 等价于真机沙箱「inst_uptime 序列数 = 0」。
    const instances = db<{ id: number; status: string }>('instances')
    for (const row of instances.list()) instances.update(row.id, { status: 'STOPPED' })

    const token = await login()
    const body = await fetchSLO(token, '?scope=platform&range=24h')
    expect(body.applicable, '无运行实例时不应声称有可用证据').toBe(false)
    expect(body.totalSamples).toBe(0)
    expect(body.upSamples).toBe(0)
    // 关键：不得给出「预算充裕」或「100% 已消耗」这类误导数字。
    expect(body.budgetAllowedSec).toBe(0)
    expect(body.budgetBurnedSec).toBe(0)
    expect(body.availability).toBe(0)
  })

  it('instance 维度：目标实例已停止 → applicable=false；运行中 → true', async () => {
    const instances = db<{ id: number; uuid: string; status: string }>('instances')
    const target = instances.find((i) => i.status === 'RUNNING')
    expect(target).toBeDefined()
    const uuid = target!.uuid

    const token = await login()
    const running = await fetchSLO(token, `?scope=instance&targetId=${uuid}&range=24h`)
    expect(running.applicable).toBe(true)

    instances.update(target!.id, { status: 'STOPPED' })
    const stopped = await fetchSLO(token, `?scope=instance&targetId=${uuid}&range=24h`)
    expect(stopped.applicable).toBe(false)
    expect(stopped.totalSamples).toBe(0)
  })

  it('node 维度：离线节点 → applicable=false；在线节点 → true', async () => {
    const nodes = db<{ id: number; uuid: string; status: number; deletedAt?: string }>('nodes')
    const online = nodes.find((n) => n.status === 1 && !n.deletedAt)
    const offline = nodes.find((n) => n.status !== 1)
    expect(online, 'seed 应至少有一个在线节点').toBeDefined()
    expect(offline, 'seed 应至少有一个离线/归档节点').toBeDefined()

    const token = await login()
    expect((await fetchSLO(token, `?scope=node&targetId=${online!.uuid}&range=24h`)).applicable).toBe(true)
    expect((await fetchSLO(token, `?scope=node&targetId=${offline!.uuid}&range=24h`)).applicable).toBe(false)
  })
})

describe('容量预测 mock（FR-464）：分档与量级正确（M1/M9）', () => {
  it('磁盘：有增长 → 给点估计 + 80% CI，且 now/limit 与同页曲线同量级', async () => {
    const token = await login()
    const { forecasts } = await fetchCapacity(token, '?scope=node&targetId=node-alpha&range=7d&metrics=node_disk_used')
    expect(forecasts).toHaveLength(1)
    const fr = forecasts[0]
    expect(fr.confidence).not.toBe('insufficient')
    expect(fr.exhaustLowDays).not.toBeNull()
    expect(fr.exhaustHighDays).not.toBeNull()
    // 与 nodeSeries 的 node_disk_used（base 120G，峰值 ~125G）/ node_disk_total（512G）一致。
    expect(fr.nowValue).toBeGreaterThan(100 * 1024 ** 3)
    expect(fr.limitValue).toBe(512 * 1024 ** 3)
    expect(fr.nowValue).toBeLessThan(fr.limitValue)
  })

  it('节点内存：now/limit 是「节点内存量级」而非 heap 量级的 2 GiB（M1 核心）', async () => {
    const token = await login()
    const { forecasts } = await fetchCapacity(token, '?scope=node&targetId=node-alpha&range=7d&metrics=node_mem_used')
    const fr = forecasts[0]
    // 修复前：limitValue 恒为 2 GiB（heap 量级）、nowValue 0.9 GiB —— 与节点内存无关。
    expect(fr.limitValue, '节点内存上限应是 GiB 以上的节点量级，不是 2 GiB heap 量级').toBeGreaterThan(4 * 1024 ** 3)
    expect(fr.nowValue).toBeGreaterThan(4 * 1024 ** 3)
    // 与 nodeSeries 的 node_mem_used / node_mem_total 同源。
    expect(fr.limitValue).toBe(16 * 1024 ** 3)
    // 该指标仍是「无增长不预测」负例。
    expect(fr.confidence).toBe('insufficient')
    expect(fr.note).toBe('无增长趋势，不预测')
    expect(fr.exhaustAt).toBeNull()
  })

  it('三种 insufficient note 在 mock 下都可复现（M9）', async () => {
    const token = await login()
    // ① 无上限来源：`node_cpu_pct` 有序列但不在 saturationMaxKey（alert_baseline.go:338）内，
    //    真后端 forecastLimit 返回 (0,false) → 「缺少容量上限…」（capacity.go:179）。
    const noLimit = (await fetchCapacity(token, '?scope=node&targetId=node-alpha&range=7d&metrics=node_cpu_pct')).forecasts[0]
    expect(noLimit.note, '缺上限的分档文案应与 capacity.go:179 一致').toContain('缺少容量上限')
    expect(noLimit.limitValue).toBe(0)
    expect(noLimit.samples, '有序列 → 样本充分，不得误报样本不足').toBeGreaterThanOrEqual(100)

    // ② 无序列（未知指标键）：真后端查不到该键的序列 → 样本数 0 → 「样本不足，不预测」。
    const noSamples = (await fetchCapacity(token, '?scope=node&targetId=node-alpha&range=7d&metrics=totally_unknown_metric')).forecasts[0]
    expect(noSamples.samples, '未知/不支持键不得伪造样本数').toBe(0)
    expect(noSamples.note).toBe('样本不足，不预测')
    expect(noSamples.nowValue).toBe(0)
    expect(noSamples.limitValue).toBe(0)

    // ③ 无增长趋势：node_mem_used 有序列有上限，但没有可外推的增长。
    const noTrend = (await fetchCapacity(token, '?scope=node&targetId=node-alpha&range=7d&metrics=node_mem_used')).forecasts[0]
    expect(noTrend.note).toBe('无增长趋势，不预测')
    expect(noTrend.nowValue).toBeGreaterThan(0)
    expect(noTrend.limitValue).toBeGreaterThan(0)
  })

  it('未知指标键不再静默降级成「看似成功」的 insufficient 成功体（M9）', async () => {
    const token = await login()
    const { forecasts } = await fetchCapacity(
      token,
      '?scope=node&targetId=node-alpha&range=7d&metrics=totally_unknown_metric',
    )
    const fr = forecasts[0]
    // 修复前：samples 恒为 1840，读起来像「采到了很多样本只是没法预测」，掩盖了「后端根本无此指标」。
    expect(fr.samples).toBe(0)
    expect(fr.note).toBe('样本不足，不预测')
    expect(fr.confidence).toBe('insufficient')
    // 不得留下任何预测数字。
    expect(fr.exhaustAt).toBeNull()
    expect(fr.exhaustLowDays).toBeNull()
    expect(fr.exhaustHighDays).toBeNull()
    expect(fr.slopePerSec).toBe(0)
  })

  it('缺 targetId → 400 INVALID_SCOPE（与 router/metric.go:800 一致）', async () => {
    const token = await login()
    const r = await fetch(`${BASE}/metrics/capacity/forecast?scope=node`, {
      headers: { Authorization: `Bearer ${token}` },
    })
    expect(r.status).toBe(400)
    expect(((await r.json()) as { error: string }).error).toBe('INVALID_SCOPE')
  })
})
