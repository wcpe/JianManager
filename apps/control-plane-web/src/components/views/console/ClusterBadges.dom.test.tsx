import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { ClusterBadges, STAT_POPOVER_MAX_ROWS } from '@/components/views/console/ClusterBadges'
import type { ClusterBadgesProps } from '@/components/views/console/ClusterBadges'

/**
 * FR-294 集群概览徽标 · 受控视图测（ADR-097 c 范式）。
 *
 * 应用侧 `ConsoleHeader.statPopovers.dom.test.tsx` 走 mock 假后端验联动；
 * 这里补的是**受控契约**：三个计数的呈现、浮窗开合上报（外壳据此开关查询）、
 * 行点击与「查看全部」的跳转参数、以及行级在线人数由注入 hook 决定。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        header: {
          onlineNodes: '在线节点', runningInstances: '运行实例', crashedInstances: '崩溃实例',
          noNodes: '暂无节点', noRunningInstances: '暂无运行实例', noCrashedInstances: '暂无崩溃实例',
          runningOnNode: '{{count}} 个运行中', moreCount: '还有 {{count}} 个',
          viewAllNodes: '查看全部节点', viewAll: '查看全部', onlinePlayersCount: '{{count}} 人在线',
        },
        metrics: { unavailable: '不可用' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

function renderBadges(over: Partial<ClusterBadgesProps> = {}) {
  const props: ClusterBadgesProps = {
    online: 3,
    running: 5,
    crashed: 0,
    nodeRows: [{ id: 1, name: 'n1', status: 1, runningCount: 2 }],
    runningRows: [{ id: 11, name: 'inst-a', nodeId: 1, nodeName: 'n1' }],
    crashedRows: [],
    remaining: { nodes: 0, running: 0, crashed: 0 },
    // 占位取数 hook：不取数（等价于「无数据」）。必填，见 ClusterBadgesProps 的说明。
    useInstancePlayers: () => undefined,
    onSlotToggle: vi.fn(),
    onOpenNode: vi.fn(),
    onOpenInstance: vi.fn(),
    onViewAll: vi.fn(),
    ...over,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <ClusterBadges {...props} />
    </I18nextProvider> as ReactNode,
  )
  return props
}

describe('ClusterBadges（FR-294 集群概览徽标）', () => {
  it('三个徽标显示计数，崩溃数大于 0 时着红。', () => {
    renderBadges({ crashed: 2 })
    expect(screen.getByRole('button', { name: '在线节点: 3' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '运行实例: 5' })).toBeInTheDocument()
    const crashed = screen.getByRole('button', { name: '崩溃实例: 2' })
    expect(within(crashed).getByText('2').className).toContain('text-status-danger')
  })

  it('打开节点浮窗上报槽位；底部「查看全部」按槽位上报。', async () => {
    const user = userEvent.setup()
    const props = renderBadges()
    await user.click(screen.getByRole('button', { name: '在线节点: 3' }))
    expect(props.onSlotToggle).toHaveBeenCalledWith('nodes', true)

    // 先点底部项：点浮窗内的行会关闭菜单，之后底部项就查不到了。
    await user.click(await screen.findByText('查看全部节点'))
    expect(props.onViewAll).toHaveBeenCalledWith('nodes')
  })

  it('点浮窗内的行带 nodeId 上报，并关闭浮窗（上报 open=false）。', async () => {
    const user = userEvent.setup()
    const props = renderBadges()
    await user.click(screen.getByRole('button', { name: '在线节点: 3' }))
    await user.click(await screen.findByText('n1'))
    expect(props.onOpenNode).toHaveBeenCalledWith(1)
    expect(props.onSlotToggle).toHaveBeenLastCalledWith('nodes', false)
  })

  it('运行中浮窗的人数由注入 hook 决定：无 hook 或无数据时不显示。', async () => {
    const user = userEvent.setup()
    renderBadges({ useInstancePlayers: () => ({ available: true, online: 7 }) })
    await user.click(screen.getByRole('button', { name: '运行实例: 5' }))
    expect(await screen.findByText('7 人在线')).toBeInTheDocument()
  })

  it('行数超上限时底部提示剩余数而非「查看全部」。', async () => {
    const user = userEvent.setup()
    renderBadges({ remaining: { nodes: 4, running: 0, crashed: 0 } })
    await user.click(screen.getByRole('button', { name: '在线节点: 3' }))
    expect(await screen.findByText('还有 4 个')).toBeInTheDocument()
  })

  it('上限常量与浮窗截断一致（8 行）。', () => {
    expect(STAT_POPOVER_MAX_ROWS).toBe(8)
  })
})
