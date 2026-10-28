import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { GaugeAttributionTooltip } from './OverviewPageView'
import type { OverviewAttributionSnapshot } from './OverviewPageView'

/**
 * FR-513：首页悬浮卡的主机级观测版式。
 *
 * 核心取舍（每条都在用例里写明原因，避免后来者「修掉」）：
 *  - **null ≠ 0**：老 Worker 未上报应显示 `--`，而不是 0；
 *  - **口径区分**：「整机 Top 进程」与「Top 受管进程」是两个不同数据源，标题必须能分辨；
 *  - **无数据不占位**：老 Worker 不报新字段时不该多出几块 `--` 噪声。
 */

function snapshot(over: Partial<OverviewAttributionSnapshot> = {}): OverviewAttributionSnapshot {
  return {
    sampledAt: '2030-01-01T00:00:00Z',
    freshness: 'fresh',
    nodes: [
      {
        nodeId: 1,
        nodeUuid: 'u1',
        name: 'node-a',
        status: 'fresh',
        cpuPct: 40,
        memoryUsedBytes: 1024,
      },
    ],
    topInstances: [],
    topProcesses: [],
    ...over,
  }
}

function renderTooltip(data: OverviewAttributionSnapshot, gauge: 'cpu' | 'load' | 'memory' = 'cpu') {
  return renderWithProviders(
    <GaugeAttributionTooltip
      gauge={gauge}
      label="CPU"
      active
      onToggle={vi.fn()}
      data={data}
      isLoading={false}
      isError={false}
    >
      <div>gauge</div>
    </GaugeAttributionTooltip>,
  )
}

describe('GaugeAttributionTooltip（FR-513 主机级观测）', () => {
  it('未上报时四处新版式都不渲染（不占位）', () => {
    // 老 Worker 不报新字段。若仍然渲染「负载三档 1m -- 5m -- 15m --」这样的空壳，
    // 悬浮卡会被一堆无意义的 `--` 塞满，比不显示更差。
    renderTooltip(snapshot())

    expect(screen.queryByText('负载三档')).toBeNull()
    expect(screen.queryByText('CPU 构成')).toBeNull()
    expect(screen.queryByText('分挂载点磁盘')).toBeNull()
    expect(screen.queryByText('整机 Top 进程')).toBeNull()
  })

  it('有数据时渲染负载三档（1m/5m/15m）', () => {
    renderTooltip(
      snapshot({
        nodes: [
          {
            nodeId: 1, nodeUuid: 'u1', name: 'node-a', status: 'fresh',
            cpuPct: 40, memoryUsedBytes: 1024,
            loadPct: 120, load5Pct: 80, load15Pct: 60,
          },
        ],
      }),
    )

    expect(screen.getByText('负载三档')).toBeInTheDocument()
    // 用可访问名定位：`5m` 与 `15m` 的可见文本互为子串，`getByText(/5m 0/)` 会同时命中两者。
    expect(screen.getByLabelText('1 分钟负载')).toHaveTextContent('120')
    expect(screen.getByLabelText('5 分钟负载')).toHaveTextContent('80')
    expect(screen.getByLabelText('15 分钟负载')).toHaveTextContent('60')
  })

  it('负载为 0 时仍渲染（0 是有效读数，不是缺测）', () => {
    // 关键区分：load5Pct=0 表示「机器完全空闲」，必须显示成 0 而不是 `--`。
    // 把 0 当成缺测会让运维以为采集坏了。
    renderTooltip(
      snapshot({
        nodes: [
          {
            nodeId: 1, nodeUuid: 'u1', name: 'node-a', status: 'fresh',
            cpuPct: 0, memoryUsedBytes: 1024, loadPct: 0, load5Pct: 0, load15Pct: 0,
          },
        ],
      }),
    )
    expect(screen.getByText('负载三档')).toBeInTheDocument()
    expect(screen.getByLabelText('5 分钟负载')).toHaveTextContent('0')
    expect(screen.getByLabelText('15 分钟负载')).toHaveTextContent('0')
  })

  it('CPU 构成只展示前 4 项 + 其余归并', () => {
    renderTooltip(
      snapshot({
        nodes: [
          {
            nodeId: 1, nodeUuid: 'u1', name: 'node-a', status: 'fresh',
            cpuPct: 40, memoryUsedBytes: 1024,
            cpuBreakdown: {
              user: 60, system: 20, idle: 10, iowait: 5,
              nice: 2, irq: 1, softirq: 1, steal: 1,
            },
          },
        ],
      }),
    )

    expect(screen.getByText('CPU 构成')).toBeInTheDocument()
    expect(screen.getByText('用户态')).toBeInTheDocument()
    expect(screen.getByText('内核态')).toBeInTheDocument()
    // 前 4 项之外的小项被归并为「其余」，不会被逐个铺开
    expect(screen.getByText('其余')).toBeInTheDocument()
    // 归并值 = 2+1+1+1 = 5%。用「其余」行的父容器定位，
    // 避免与 user 60% 等其它百分比混淆（getByText('5.0%') 会多命中）。
    const otherRow = screen.getByText('其余').closest('div')
    expect(otherRow).toHaveTextContent('5.0%')
  })

  it('分挂载点磁盘全部展示，且高使用率项突出', () => {
    renderTooltip(
      snapshot({
        nodes: [
          {
            nodeId: 1, nodeUuid: 'u1', name: 'node-a', status: 'fresh',
            cpuPct: 40, memoryUsedBytes: 1024,
            mounts: [
              { mountpoint: '/var', totalBytes: 100, usedBytes: 95, usedPct: 95 },
              { mountpoint: '/', totalBytes: 100, usedBytes: 14, usedPct: 14.4 },
            ],
          },
        ],
      }),
    )

    expect(screen.getByText('分挂载点磁盘')).toBeInTheDocument()
    expect(screen.getByText('/var')).toBeInTheDocument()
    expect(screen.getByText('95.0%')).toBeInTheDocument()
    expect(screen.getByText('14.4%')).toBeInTheDocument()
  })

  it('整机 Top 进程与「Top 受管进程」标题可区分', () => {
    // 这是本 FR 最容易被误读的地方：两份数据源完全不同。
    // 整机 Top 含系统进程与别人的进程；受管 Top 只看我们托管的实例进程树。
    renderTooltip(
      snapshot({
        hostTopProcesses: [
          { nodeId: 1, nodeName: 'node-a', pid: 42, comm: 'java', cpuPct: 523, rssBytes: 1024 },
        ],
      }),
    )

    expect(screen.getByText('整机 Top 进程')).toBeInTheDocument()
    // 受管那份标题是「Top 受管进程」，两者文案不同——用例锁住这一点
    expect(screen.getByText(/Top 受管进程/)).toBeInTheDocument()
    expect(screen.getByText('java')).toBeInTheDocument()
  })

  it('进程数渲染为「运行 / 总」', () => {
    renderTooltip(
      snapshot({
        nodes: [
          {
            nodeId: 1, nodeUuid: 'u1', name: 'node-a', status: 'fresh',
            cpuPct: 40, memoryUsedBytes: 1024, procsRunning: 32, procsTotal: 11023,
          },
        ],
      }),
    )
    expect(screen.getByText(/进程数/)).toBeInTheDocument()
    expect(screen.getByText(/32 \/ 11023/)).toBeInTheDocument()
  })
})
