import { describe, it, expect, beforeEach } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * FR-313：崩溃诊断卡——实例控制台概览区的崩溃快照列表。
 * 数据来自 GET /instances/:id/crash-snapshots（devmock 种子：实例 3 两条、实例 2 无），
 * 覆盖列表渲染（倒序 + 退出码/信号/时长）、尾部输出展开（等宽 pre）、空态。
 */
describe('InstanceConsolePage 崩溃诊断卡（FR-313）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('渲染快照列表：倒序（最新在前）、含退出码/信号/运行时长', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const card = await screen.findByTestId('crash-diagnostics')
    // 快照异步到达；无快照时该段只显示一行灰字（FR-423 空态收缩），标题随数据出现。
    expect(await within(card).findByText('崩溃诊断')).toBeInTheDocument()

    const rows = await within(card).findAllByRole('button', { expanded: false })
    expect(rows).toHaveLength(2)
    // 种子里 07-12（exit 137）晚于 07-10（exit 1），倒序应最新在前。
    expect(rows[0]).toHaveTextContent('退出码 137')
    expect(rows[0]).toHaveTextContent('信号 killed')
    expect(rows[0]).toHaveTextContent('运行时长 12m 34s')
    expect(rows[1]).toHaveTextContent('退出码 1')
    expect(rows[1]).toHaveTextContent('运行时长 2.3s')
    // 非信号退出不显示信号 pill。
    expect(rows[1]).not.toHaveTextContent('信号')
  })

  it('点击行展开尾部输出（等宽字体 pre），再点收起', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const card = await screen.findByTestId('crash-diagnostics')
    const rows = await within(card).findAllByRole('button', { expanded: false })

    // 展开前尾部输出的 <pre> 不可见（FR-470 起证据行/指纹会出现在卡片其它位置，
    // 故此处按「承载尾部输出的 pre」判定，而不是全文匹配）。
    expect(within(card).queryByText(/Saving chunks/)).not.toBeInTheDocument()

    await user.click(rows[1])
    expect(rows[1]).toHaveAttribute('aria-expanded', 'true')
    // 等宽字体承载（spec §2）：尾部输出渲染在 font-mono 的 pre 中。
    // FR-470 起同一行文本也可能出现在「命中证据」列表里，故按元素类型定位 pre。
    const pre = within(card)
      .getAllByText(/Unable to access jarfile paper.jar/)
      .find((el) => el.tagName === 'PRE')
    expect(pre).toBeDefined()
    expect(pre?.className).toContain('font-mono')

    // 再点收起。
    await user.click(rows[1])
    expect(within(card).queryByText(/Saving chunks/)).not.toBeInTheDocument()
  })

  it('无快照实例显示空态文案', async () => {
    // 实例 2（lobby-proxy）种子无崩溃快照。
    renderWithProviders(<InstanceConsolePage instanceId={2} />, { route: '/instances/2' })

    const card = await screen.findByTestId('crash-diagnostics')
    expect(await within(card).findByText('暂无崩溃记录，进程非正常退出时会自动留存现场')).toBeInTheDocument()
  })
})

/**
 * FR-470：崩溃诊断增强——根因标签 / 证据 / 关联资源证据 / 趋势 / 同类聚合。
 * 种子（devmock）：实例 3 两条快照分别带 class_not_found 与 oom（后者带关联证据）。
 */
describe('InstanceConsolePage 崩溃诊断增强（FR-470）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('每条崩溃显示根因标签，展开可见命中证据与关联资源证据', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const card = await screen.findByTestId('crash-diagnostics')
    const rows = await within(card).findAllByRole('button', { expanded: false })
    expect(rows).toHaveLength(2)

    // 根因标签直接出现在折叠态（运维不必展开 200 行日志才知道是什么崩的）。
    const labels = within(card).getAllByTestId('crash-root-cause')
    expect(labels.map((el) => el.getAttribute('data-cause'))).toEqual(
      expect.arrayContaining(['oom', 'class_not_found']),
    )

    // 展开含 oom 的那条：证据区 + 关联资源证据可见。
    const oomRow = rows.find((r) => r.textContent?.includes('退出码 137'))
    // W-18：不用非空断言绑定种子文案——种子一改就是 TypeError 而非清晰的断言失败。
    expect(oomRow, '种子应含退出码 137 的快照（oom + killed）').toBeDefined()
    await user.click(oomRow as HTMLElement)
    expect(within(card).getByTestId('crash-correlation')).toHaveTextContent('崩前 RSS 峰值')
    expect(within(card).getByTestId('crash-correlation')).toHaveTextContent('逼近内存上限')
  })

  it('趋势与同类聚合来自独立汇总表（连崩超过 K=5 仍完整）', async () => {
    // 实例 12（survival-world）种子：快照 6 条 → 列表被 K=5 裁到 5 条；趋势仍报完整 6 次。
    // **这条断言必须能失败**：若前端把趋势来源改回快照列表（或 mock 不裁列表），
    // 「列表 5 < 趋势 6」就不再成立，用例即红（原先 mock 未建模 K=5，该断言恒真=假绿）。
    renderWithProviders(<InstanceConsolePage instanceId={12} />, { route: '/instances/12' })

    const card = await screen.findByTestId('crash-diagnostics')
    const trend = await within(card).findByTestId('crash-trend')
    // 趋势标题带窗口天数，总数来自汇总表（独立于被裁剪的快照列表）。
    expect(trend).toHaveTextContent('近 30 天崩溃趋势')
    expect(trend).toHaveTextContent('共 6 次')

    // 列表侧只剩 K=5 条 —— 与趋势总数形成差异，正是「趋势不受裁剪影响」的可观察证据。
    const rows = within(card).getAllByRole('button', { expanded: false })
    expect(rows).toHaveLength(5)
    const listTotal = rows.length
    expect(listTotal).toBeLessThan(6)

    // 同类聚合（按指纹分组）给出**内容**：两个指纹各自计数，不是「元素存在」而已（W-18）。
    const aggregate = within(trend).getByTestId('crash-signature-aggregate')
    expect(within(aggregate).getByText('Java heap space')).toBeInTheDocument()
    expect(within(aggregate).getByText('GC overhead limit exceeded')).toBeInTheDocument()
    expect(within(aggregate).getByText('Address already in use')).toBeInTheDocument()
    // 按指纹求和：4 + 1 + 1 = 6 = 趋势总数（与统计行逐条对齐）。
    expect(aggregate).toHaveTextContent('×4')
    expect(aggregate).toHaveTextContent('×1')
  })

  it('趋势迷你图按天求和（同一天多根因合计），且各柱之和等于总数', async () => {
    // 职责边界（诚实说明）：**服务端的 (天 × 根因) 聚合契约由 mock 侧单测保证**
    // （packages/devmock/src/instanceP3Semantics.test.ts 断言「同天同因只出一个点」），
    // 前端在 CrashTrendPanel 里还会按天自行求和，故本条只验证**前端这一层的求和**：
    // 实例 12 三个统计日各含 2 次崩溃（其中两天各由两个指纹各 1 次组成）。
    // 若前端改成 `byDay.set(p.day, p.count)`（覆盖而非累加），多根因那天会显示 1 而非 2 → 本用例变红。
    renderWithProviders(<InstanceConsolePage instanceId={12} />, { route: '/instances/12' })

    const card = await screen.findByTestId('crash-diagnostics')
    const trend = await within(card).findByTestId('crash-trend')
    // 迷你图每根柱对应一天（data-day / data-count 由 CrashTrendPanel 显式下发）。
    const bars = [...within(trend).getByTestId('crash-trend-bars').querySelectorAll('span[data-day]')]
    expect(bars.length, '实例 12 应有 3 个统计日').toBe(3)

    const dayCounts = bars.map((b) => Number(b.getAttribute('data-count')))
    // 每天 2 次（day-4 是「两根因各 1 次」的合计 → 只取一条会显示 1；day-1 是 count=2 的直接累加）。
    expect(dayCounts).toEqual([2, 2, 2])
    // 各柱之和恒等于趋势总数。
    expect(dayCounts.reduce((a, b) => a + b, 0)).toBe(6)
    expect(trend).toHaveTextContent('共 6 次')
    // 时区口径必须标注（W-12）：与同卡片快照行的本地时间并列时不可沉默。
    expect(within(trend).getByTestId('crash-trend-basis')).toHaveTextContent('UTC')
  })

  it('无崩溃实例不渲染趋势区（不多打接口、不占版面）', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={2} />, { route: '/instances/2' })

    const card = await screen.findByTestId('crash-diagnostics')
    expect(await within(card).findByText('暂无崩溃记录，进程非正常退出时会自动留存现场')).toBeInTheDocument()
    expect(within(card).queryByTestId('crash-trend')).not.toBeInTheDocument()
  })
})
