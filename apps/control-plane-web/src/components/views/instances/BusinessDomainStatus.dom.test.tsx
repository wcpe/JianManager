import { describe, it, expect } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import type { BusinessStatus } from '@/api/businessStatus'
import type { BusinessManifest } from '@/lib/console/business'
import BusinessDomainStatus from './BusinessDomainStatus'

/** 
 *业务插件域状态面板 · 受控视图测（ADR-103 a 范式）。
 *
 * 用例覆盖 spec §8 的功能验收：有域（正常/降级/原因/计数）、无域空态、
 * stale=true 提示、manifest 展开与免责声明。
 *
 * 断言锚在 data-testid 与业务文案上：本面板几乎全是文案面，没有可供断言的交互状态，
 * 故直接从 zh.json 取原文（同仓既有 dom 测试的做法）。
 */

/** 构造一份域状态（默认 probeConnected、非 stale，用例按需覆盖）。 */
function statusWith(overrides: Partial<BusinessStatus> = {}): BusinessStatus {
  return {
    instanceId: 1,
    instanceName: 'game-01',
    probeConnected: true,
    fetchedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
    stale: false,
    domains: [],
    ...overrides,
  }
}

/** 一份 manifest：只用于「查看 manifest」抽屉展示原文。 */
const manifest: BusinessManifest = {
  domains: {
    activity: {
      actions: [
        { action: 'recent', args: ['player'], readOnly: true },
        { action: 'grant', args: ['player', 'id'], readOnly: false },
      ],
    },
    economy: {
      actions: [{ action: 'balance', args: ['player', 'currency'], readOnly: true }],
    },
  },
}

describe('BusinessDomainStatus（域状态面板）', () => {
  it('正常域显示域名、状态徽章与调用/失败计数', () => {
    renderWithProviders(
      <BusinessDomainStatus
        status={statusWith({
          domains: [
            {
              domain: 'activity',
              available: true,
              manifestDigest: 'a3f2b8c1d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f',
              lastDispatchAt: Math.floor(Date.now() / 1000) - 120,
              dispatchCount: 1523,
              failureCount: 7,
            },
            {
              domain: 'inventory',
              available: true,
              lastDispatchAt: Math.floor(Date.now() / 1000) - 600,
              dispatchCount: 88,
              failureCount: 0,
            },
          ],
        })}
        onRefresh={() => {}}
      />,
    )

    // 标题带域数量（spec §4.2 的「业务插件（3）」形态）。
    expect(screen.getByText('业务插件（2）')).toBeInTheDocument()

    const activity = screen.getByTestId('business-domain-activity')
    expect(within(activity).getByText('activity')).toBeInTheDocument()
    expect(within(activity).getByText('正常')).toBeInTheDocument()
    expect(within(activity).getByText('调用 1523 次 / 失败 7 次')).toBeInTheDocument()
    // 最近调用按相对时间渲染（2 分钟前）。
    expect(within(activity).getByText(/最近调用 2 分钟前/)).toBeInTheDocument()
    // manifest 指纹按 spec 截成「前八位 + 省略号」。
    expect(within(activity).getByText('manifest a3f2b8c1…')).toBeInTheDocument()

    const inventory = screen.getByTestId('business-domain-inventory')
    expect(within(inventory).getByText('正常')).toBeInTheDocument()
    expect(within(inventory).getByText('调用 88 次 / 失败 0 次')).toBeInTheDocument()
    // 无指纹时不显示 digest 行。
    expect(within(inventory).queryByText(/^manifest /)).not.toBeInTheDocument()
  })

  it('降级域醒目标记并显示原因', () => {
    renderWithProviders(
      <BusinessDomainStatus
        status={statusWith({
          domains: [
            {
              domain: 'economy',
              available: false,
              degradedReason: 'PLUGIN_DISABLED',
              lastDispatchAt: 0,
              dispatchCount: 0,
              failureCount: 0,
            },
          ],
        })}
        onRefresh={() => {}}
      />,
    )

    const economy = screen.getByTestId('business-domain-economy')
    // 该行带降级标记（data-available=false，样式侧挂左侧色条）。
    expect(economy).toHaveAttribute('data-available', 'false')
    expect(within(economy).getByText('降级')).toBeInTheDocument()
    // 原因原样透传，平台不改写不翻译。
    expect(within(economy).getByText('原因：PLUGIN_DISABLED')).toBeInTheDocument()
    // 计数为 0 → 计数行不渲染，改为「从未调用」。
    expect(within(economy).queryByText(/调用 0 次/)).not.toBeInTheDocument()
    expect(within(economy).getByText('从未调用')).toBeInTheDocument()
  })

  it('stale=true 显示「探针不可达，显示最后一次已知状态」提示', () => {
    renderWithProviders(
      <BusinessDomainStatus
        status={statusWith({
          probeConnected: false,
          stale: true,
          error: '节点未连接',
          domains: [
            {
              domain: 'activity',
              available: true,
              lastDispatchAt: Math.floor(Date.now() / 1000) - 60,
              dispatchCount: 10,
              failureCount: 0,
            },
          ],
        })}
        onRefresh={() => {}}
      />,
    )

    const stale = screen.getByTestId('business-domain-stale')
    expect(within(stale).getByText(/探针不可达，显示最后一次已知状态/)).toBeInTheDocument()
    // 后端给的降级原因原样透出（排障第一手线索）。
    expect(within(stale).getByText('原因：节点未连接')).toBeInTheDocument()
    // 过期数据照常渲染：面板不因 stale 变空。
    expect(screen.getByTestId('business-domain-activity')).toBeInTheDocument()
  })

  it('stale=false 时不显示过期提示', () => {
    renderWithProviders(
      <BusinessDomainStatus
        status={statusWith({
          domains: [
            { domain: 'activity', available: true, lastDispatchAt: 0, dispatchCount: 1, failureCount: 0 },
          ],
        })}
        onRefresh={() => {}}
      />,
    )
    expect(screen.queryByTestId('business-domain-stale')).not.toBeInTheDocument()
  })

  it('无域时显示空状态与排查提示（spec §4.3）', () => {
    renderWithProviders(<BusinessDomainStatus status={statusWith()} onRefresh={() => {}} />)

    expect(screen.getByText('未注册业务插件')).toBeInTheDocument()
    expect(screen.getByText(/该服务器的探针未检测到任何已注册的业务域/)).toBeInTheDocument()
    // 排查方向必须出现，否则空态只说了「没有」，没说「怎么办」。
    expect(screen.getByText(/插件是否已安装 \/ 探针版本是否支持自注册/)).toBeInTheDocument()
    expect(screen.queryByTestId('business-domain-list')).not.toBeInTheDocument()
  })

  it('展开 manifest 显示原始 JSON 与「平台不解释」免责声明', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <BusinessDomainStatus
        manifest={manifest}
        status={statusWith({
          domains: [
            {
              domain: 'activity',
              available: true,
              manifestDigest: 'a3f2b8c1d4e5f6071829',
              lastDispatchAt: 0,
              dispatchCount: 3,
              failureCount: 1,
            },
          ],
        })}
        onRefresh={() => {}}
      />,
    )

    // 默认收起：没有免责声明与 JSON。
    expect(screen.queryByTestId('business-domain-manifest')).not.toBeInTheDocument()
    expect(screen.queryByText('以下内容由插件声明，平台不解释其语义。')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /查看 manifest/ }))

    const drawer = screen.getByTestId('business-domain-manifest')
    // spec §4.2 明文要求的免责声明。
    expect(within(drawer).getByText('以下内容由插件声明，平台不解释其语义。')).toBeInTheDocument()
    // 内容为该域在 manifest 里的原文（只 stringify，不做语义解释）。
    expect(within(drawer).getByText(/"action": "recent"/)).toBeInTheDocument()
    expect(within(drawer).getByText(/"readOnly": false/)).toBeInTheDocument()
    // 不混入其他域的内容。
    expect(within(drawer).queryByText(/balance/)).not.toBeInTheDocument()
  })

  it('manifest 里没有该域时不渲染展开入口（不展示空原文）', () => {
    renderWithProviders(
      <BusinessDomainStatus
        manifest={manifest}
        status={statusWith({
          domains: [
            {
              domain: 'not-declared',
              available: true,
              manifestDigest: 'deadbeefdeadbeef',
              lastDispatchAt: 0,
              dispatchCount: 1,
              failureCount: 0,
            },
          ],
        })}
        onRefresh={() => {}}
      />,
    )

    const row = screen.getByTestId('business-domain-not-declared')
    expect(within(row).queryByRole('button', { name: /查看 manifest/ })).not.toBeInTheDocument()
  })

  it('加载中显示 loading，不外露空态', () => {
    renderWithProviders(<BusinessDomainStatus loading onRefresh={() => {}} />)

    expect(screen.getByText('加载中...')).toBeInTheDocument()
    expect(screen.queryByText('未注册业务插件')).not.toBeInTheDocument()
  })

  it('查询失败显示错误文案', () => {
    renderWithProviders(<BusinessDomainStatus error onRefresh={() => {}} />)

    expect(screen.getByText('读取业务域状态失败')).toBeInTheDocument()
    expect(screen.queryByText('未注册业务插件')).not.toBeInTheDocument()
  })
})
