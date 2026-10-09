import { describe, it, expect } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import NodePortsPanel from './NodePortsPanel'
import type { NodePorts } from '@jianmanager/ui/lib/node-ports'

/**
 * 本文件自带局部 i18n 实例，而不复用 `src/test/i18n.tsx` 的共享实例。
 *
 * 原因：共享实例只备了 `ReleaseNotes` 用到的三条键，而本组件需要整个 `ports.*`
 * 命名空间才能验证「分配范围」的插值渲染。把 7 条 ports 键塞进共享实例会让它
 * 变成第二份应用语言包（该文件已明确反对）；就近自包含则把「本组件要求消费方
 * 提供哪些键」写在了使用它的测试里。真实语言包由消费方提供（主控台 zh/en）。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        ports: {
          range: '分配范围：server {{server}}+（每段 {{size}} 个）',
          instance: '实例',
          role: '角色',
          serverPort: '监听端口',
          queryPort: '查询端口',
          filterPlaceholder: '按实例名过滤',
          noMatch: '无匹配端口',
          empty: '暂无端口占用',
        },
        networks: { role_proxy: '代理', role_backend: '后端' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 受控渲染：数据与加载态直接经 props 注入（ADR-097 a 范式），无需 MSW、无需 QueryClient。 */
function renderPanel(data?: NodePorts, isLoading?: boolean) {
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  return render(<NodePortsPanel data={data} isLoading={isLoading} />, { wrapper: Wrapper })
}

const basicData: NodePorts = {
  nodeId: 42,
  ranges: { serverPortBase: 25565, rangeSize: 2000 },
  occupied: [
    { instanceId: 1, name: 'survival-proxy', role: 'proxy', serverPort: 25565, queryPort: 0 },
    { instanceId: 2, name: 'survival-lobby', role: 'backend', serverPort: 25566, queryPort: 25566 },
  ],
}

describe('NodePortsPanel（FR-032 端口占用 · ADR-097 a 范式）', () => {
  it('加载态渲染骨架占位而非裸文字', () => {
    const { container } = renderPanel(undefined, true)
    // 骨架：范围行 + 两行表格轮廓；此时不应出现表头（数据未就绪）。
    expect(container.querySelectorAll('[data-slot="skeleton"]').length).toBe(3)
    expect(screen.queryByText('实例')).not.toBeInTheDocument()
  })

  it('渲染节点端口分配范围与 proxy/backend 占用行', async () => {
    renderPanel(basicData)

    expect(await screen.findByText('分配范围：server 25565+（每段 2000 个）')).toBeInTheDocument()
    expect(screen.getByText('实例')).toBeInTheDocument()
    expect(screen.getByText('角色')).toBeInTheDocument()
    expect(screen.getByText('监听端口')).toBeInTheDocument()
    expect(screen.getByText('survival-proxy')).toBeInTheDocument()
    expect(screen.getByText('survival-lobby')).toBeInTheDocument()
    expect(screen.getByText('代理')).toBeInTheDocument()
    expect(screen.getByText('后端')).toBeInTheDocument()
    expect(screen.getByText('25565')).toBeInTheDocument()
    expect(screen.getAllByText('25566').length).toBeGreaterThanOrEqual(1)
  })

  it('实例名过滤收敛端口行', async () => {
    const user = userEvent.setup()
    const data: NodePorts = {
      ...basicData,
      occupied: [
        { instanceId: 1, name: 'survival-proxy', role: 'proxy', serverPort: 25565, queryPort: 0 },
        { instanceId: 2, name: 'creative-lobby', role: 'backend', serverPort: 25566, queryPort: 25566 },
      ],
    }
    renderPanel(data)
    await screen.findByText('survival-proxy')

    // 过滤 'creative' → 仅 creative-lobby 命中，survival-proxy 被过滤掉。
    await user.type(screen.getByRole('searchbox'), 'creative')
    await waitFor(() => {
      expect(screen.queryByText('survival-proxy')).not.toBeInTheDocument()
    })
    expect(screen.getByText('creative-lobby')).toBeInTheDocument()
  })

  it('无数据时渲染空态行', async () => {
    renderPanel({ nodeId: 42, ranges: { serverPortBase: 25565, rangeSize: 2000 }, occupied: [] })
    expect(await screen.findByText('暂无端口占用')).toBeInTheDocument()
  })

  it('大量端口占用只挂可视窗口（虚拟化后 DOM 行受限）', async () => {
    const occupied = Array.from({ length: 300 }, (_, i) => ({
      instanceId: i + 1,
      name: `srv-${i + 1}`,
      role: 'backend',
      serverPort: 25565 + i,
      queryPort: 0,
    }))
    renderPanel({ nodeId: 42, ranges: { serverPortBase: 25565, rangeSize: 2000 }, occupied })

    const surface = await screen.findByTestId('node-ports-virtual')
    await screen.findByText('srv-1')
    // 虚拟化：数据行（含名称单元格）远少于 300；只挂可视窗口 + overscan。
    const dataRows = within(surface)
      .getAllByRole('row')
      .filter((r) => within(r).queryByText(/^srv-\d+$/))
    expect(dataRows.length).toBeLessThan(50)
  })
})
