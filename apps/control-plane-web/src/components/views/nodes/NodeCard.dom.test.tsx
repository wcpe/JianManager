import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import type { NodeInfo } from '@/lib/nodes/node-types'
import { NodeCard } from './NodeListParts'

const node: NodeInfo = {
  id: 91, uuid: 'node-unknown', name: 'unknown', host: '', grpcPort: 0, wsPort: 0,
  status: 1, maintenance: false, tunnelConnected: false, os: '', arch: '',
  cpuCores: 0, memoryMb: 0, diskTotalMb: 0,
  cpuUsage: Number.NaN, memoryUsage: Number.NaN, diskUsage: Number.NaN,
  networkBytesSent: 0, networkBytesRecv: 0, loadAvg1: Number.NaN,
  lastHeartbeat: 'invalid-time', createdAt: '',
}

describe('NodeCard 数据可用性', () => {
  it('在线但指标或容量未知时使用占位，不捏造零值', () => {
    renderWithProviders(<NodeCard node={node} instanceCount={3} onOpen={() => {}} />)
    const card = screen.getByTestId('node-card-91')
    expect(card).toHaveTextContent('CPU --')
    expect(card).toHaveTextContent('内存 --')
    expect(card).toHaveTextContent('磁盘 --')
    expect(card).toHaveTextContent('负载 --')
    expect(card).toHaveTextContent('心跳 --')
    expect(card).not.toHaveTextContent('0%')
    expect(card).not.toHaveTextContent('NaN')
    expect(card).not.toHaveTextContent('0 B')
  })

  it('离线节点保留硬件规格，但不把陈旧指标当成实时读数', () => {
    renderWithProviders(<NodeCard node={{ ...node, status: 0, cpuCores: 8, memoryMb: 32768, diskTotalMb: 512000, cpuUsage: 0.8, memoryUsage: 0.5, diskUsage: 0.4, loadAvg1: 2.5 }} instanceCount={3} onOpen={() => {}} />)
    const card = screen.getByTestId('node-card-91')
    expect(card).toHaveTextContent('8 核')
    expect(card).toHaveTextContent('32.0 GB')
    expect(card).toHaveTextContent('CPU --')
    expect(card).toHaveTextContent('内存 --')
    expect(card).toHaveTextContent('磁盘 --')
    expect(card).toHaveTextContent('负载 --')
    expect(card).not.toHaveTextContent('80%')
    expect(card).not.toHaveTextContent('直拨')
  })
})
