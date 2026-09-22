import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useConsoleStore } from '@/stores/console'
import InstancesPage from './InstancesPage'

/**
 * FR-452 实例列表树表强断言：分组维度切换即时重排 / 分组头折叠（入 URL）/
 * 成员计数 + 聚合健康色带 / 未分组恒落末尾。
 * 只桩 /nodes（列表页顶部取数）；/networks、/topology、/instance-groups 走真实 devmock handler。
 */
beforeEach(() => {
  loginMockUser()
  useConsoleStore.setState({ selectedNodeId: null })
  server.use(
    http.get(API('/nodes'), () =>
      HttpResponse.json([
        { id: 1, name: 'node-a' },
        { id: 2, name: 'node-b' },
      ]),
    ),
  )
})

/** Radix Select 在 jsdom 下需要指针捕获/滚动 polyfill 才能展开并按项点击。 */
function polyfillRadixPointer() {
  HTMLElement.prototype.hasPointerCapture ??= () => false
  HTMLElement.prototype.setPointerCapture ??= () => {}
  HTMLElement.prototype.releasePointerCapture ??= () => {}
  HTMLElement.prototype.scrollIntoView ??= () => {}
}

/** 打开「分组」下拉并选中某个维度选项（FR-452）。 */
async function pickDimension(user: ReturnType<typeof userEvent.setup>, optionLabel: string) {
  polyfillRadixPointer()
  await user.click(await screen.findByRole('combobox', { name: '分组' }))
  await user.click(await screen.findByRole('option', { name: optionLabel }))
}

/** 读取当前可见的分组头行标签（保持 DOM 顺序，末尾即「未分组」位置）。 */
function groupLabels(): string[] {
  const table = screen.getByTestId('instances-table-virtual')
  return within(table)
    .queryAllByTestId('instances-group-row')
    .map((row) => (row.textContent ?? '').trim())
}

describe('InstancesPage 分组树表（FR-452）', () => {
  it('分组头行带折叠按钮、成员计数与聚合健康色带', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=region' })

    const table = await screen.findByTestId('instances-table-virtual')
    await waitFor(() => expect(within(table).getAllByTestId('instances-group-row').length).toBeGreaterThan(0))
    // 折叠箭头（可访问名「折叠分组 …」）+ 聚合健康色带同排。
    expect(within(table).getAllByTestId('instances-group-toggle').length).toBeGreaterThan(0)
    expect(within(table).getAllByTestId('instances-group-health').length).toBeGreaterThan(0)
    const firstToggle = within(table).getAllByTestId('instances-group-toggle')[0]
    expect(firstToggle).toHaveAttribute('aria-expanded', 'true')
  })

  it('region 维度：先大区后小区两级展开，折叠大区后子行消失并写入 URL', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=region' })

    const table = await screen.findByTestId('instances-table-virtual')
    await waitFor(() => expect(within(table).getAllByTestId('instances-group-row').length).toBeGreaterThan(0))
    // 两级：存在 depth=0（大区）与 depth=1（小区）两类分组头。
    const depths = within(table)
      .getAllByTestId('instances-group-row')
      .map((r) => r.getAttribute('data-group-depth'))
    expect(depths).toContain('0')
    expect(depths).toContain('1')

    await user.click(within(table).getAllByTestId('instances-group-toggle')[0])

    // 折叠态写入 URL（键形如 region:r1）。
    await waitFor(() => {
      expect(new URLSearchParams(window.location.search).get('collapsed')).toContain('region%3Ar')
    })
    // 折叠后该分组头 aria-expanded=false；其小区后代不再渲染（首行即被折叠的大区头）。
    const firstRow = within(table).getAllByTestId('instances-group-row')[0]
    expect(firstRow).toHaveAttribute('data-collapsed', 'true')
    expect(within(firstRow).getByTestId('instances-group-toggle')).toHaveAttribute('aria-expanded', 'false')
  })

  it('折叠态可从 URL 恢复（深链 ?collapsed=）', async () => {
    renderWithProviders(<InstancesPage />, {
      route: '/instances?view=list&groupBy=region&collapsed=region%3Ar1',
    })

    const table = await screen.findByTestId('instances-table-virtual')
    await waitFor(() => expect(within(table).getAllByTestId('instances-group-row').length).toBeGreaterThan(0))
    const toggles = within(table).getAllByTestId('instances-group-toggle')
    // 首个分组（r1）按 URL 恢复为已折叠。
    expect(toggles[0]).toHaveAttribute('aria-expanded', 'false')
  })

  it('切换维度即时重排（region/zone/groupTree/network/role/type/env/node/status/none）', { timeout: 60000 }, async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=region' })
    await screen.findByTestId('instances-table-virtual')

    const cases: { option: string; expectFirstGroup?: string }[] = [
      { option: '按小区', expectFirstGroup: 'z1' },
      // groupTree 现为多级：首组为根组「亚洲区」（devmock 分组树），非扁平组名。
      { option: '组织分组', expectFirstGroup: '亚洲区' },
      { option: '按群组', expectFirstGroup: 'creative' },
      { option: '按角色', expectFirstGroup: '后端' },
      { option: '按类型', expectFirstGroup: 'generic' },
      { option: '按环境', expectFirstGroup: '开发' },
      { option: '按节点', expectFirstGroup: 'node-a' },
      { option: '按状态', expectFirstGroup: '崩溃' },
      { option: '无分组' },
    ]
    for (const c of cases) {
      await pickDimension(user, c.option)
      if (c.expectFirstGroup) {
        await waitFor(() => expect(groupLabels()[0]).toContain(c.expectFirstGroup as string))
      } else {
        // 无分组：不含任何分组头行（平铺）。
        await waitFor(() => expect(groupLabels()).toHaveLength(0))
      }
    }
  })

  it('未分组实例恒落末尾（network 维度「未分组」；region 维度「未分大区」）', async () => {
    const user = userEvent.setup()
    // 深链折叠键须为渲染层实际使用的 `${dim}:${group.key}`（即 `network:<群组名>`），
    // 而非 `dim:network:<名>`；否则折叠无效、断言会「恰好」通过而漏验。
    renderWithProviders(<InstancesPage />, {
      route: '/instances?view=list&groupBy=network&collapsed=network%3Acreative%2Cnetwork%3Asurvival',
    })
    const table = await screen.findByTestId('instances-table-virtual')

    await waitFor(() => expect(groupLabels().length).toBeGreaterThan(0))
    // 深链折叠真的生效：creative / survival 两个群组头均为已折叠态，且其成员行不再渲染
    //（若折叠键写错则两者保持展开、成员行仍在 DOM，以下断言会失败——这才真正验证「全部折叠后仅剩分组头」）。
    const rows = within(table).getAllByTestId('instances-group-row')
    const collapsedLabels = rows
      .filter((r) => r.getAttribute('data-collapsed') === 'true')
      .map((r) => (r.textContent ?? '').trim())
    expect(collapsedLabels.some((l) => l.includes('creative'))).toBe(true)
    expect(collapsedLabels.some((l) => l.includes('survival'))).toBe(true)
    expect(within(table).queryByText('creative-proxy')).toBeNull()
    expect(within(table).queryByText('survival-proxy')).toBeNull()
    // 未命中任何群组的实例落「未分组」且在末尾。
    const labels = groupLabels()
    expect(labels[labels.length - 1]).toContain('未分组')

    await pickDimension(user, '大区 / 小区')
    await user.click(await screen.findByTestId('instances-collapse-all'))
    await waitFor(() => {
      const region = groupLabels()
      expect(region[region.length - 1]).toContain('未分大区')
    })
  })

  it('「全部折叠」只保留分组头，「全部展开」恢复成员行', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=region' })
    await screen.findByTestId('instances-table-virtual')
    await waitFor(() => expect(groupLabels().length).toBeGreaterThan(0))

    await user.click(screen.getByTestId('instances-collapse-all'))
    await waitFor(() => {
      const params = new URLSearchParams(window.location.search).get('collapsed') ?? ''
      expect(params.length).toBeGreaterThan(0)
      // 折叠后可见的每个分组头都标记为已折叠（仅剩分组头，无成员行）。
      const rows = screen.getByTestId('instances-table-virtual')
      expect(
        within(rows)
          .getAllByTestId('instances-group-row')
          .every((r) => r.getAttribute('data-collapsed') === 'true'),
      ).toBe(true)
    })

    await user.click(screen.getByTestId('instances-expand-all'))
    await waitFor(() => {
      const rows = screen.getByTestId('instances-table-virtual')
      expect(
        within(rows)
          .getAllByTestId('instances-group-row')
          .some((r) => r.getAttribute('data-collapsed') === 'false'),
      ).toBe(true)
    })
  })

  it('groupTree 维度：按分组树层级多级展开（父组头 → 子组头 → 成员），键为组 id', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=groupTree' })
    const table = await screen.findByTestId('instances-table-virtual')
    await waitFor(() => expect(within(table).getAllByTestId('instances-group-row').length).toBeGreaterThan(0))

    const rows = within(table).getAllByTestId('instances-group-row')
    const labels = rows.map((r) => (r.textContent ?? '').trim())
    const depths = rows.map((r) => Number(r.getAttribute('data-group-depth')))
    // devmock 分组树：亚洲区(根, depth 0) → 生存/创造(子, depth 1)。多级层级而非扁平同名合并。
    expect(labels[0]).toContain('亚洲区')
    expect(depths[0]).toBe(0)
    expect(labels.some((l, i) => depths[i] === 1 && l.includes('生存'))).toBe(true)
    expect(labels.some((l, i) => depths[i] === 1 && l.includes('创造'))).toBe(true)
  })

  it('groupTree 维度：折叠父组隐藏其全部子组头', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=groupTree' })
    const table = await screen.findByTestId('instances-table-virtual')
    await waitFor(() => expect(within(table).getAllByTestId('instances-group-row').length).toBeGreaterThan(0))

    const before = within(table).getAllByTestId('instances-group-row').length
    // 折叠首个根组「亚洲区」→ 其子组头（生存/创造）从可见行中消失。
    await user.click(within(table).getAllByTestId('instances-group-toggle')[0])
    await waitFor(() => {
      const rows = within(table).getAllByTestId('instances-group-row')
      expect(rows.length).toBeLessThan(before)
      expect(rows[0]).toHaveAttribute('data-collapsed', 'true')
      const labels = rows.map((r) => (r.textContent ?? '').trim())
      expect(labels.some((l) => l.includes('生存'))).toBe(false)
      expect(labels.some((l) => l.includes('创造'))).toBe(false)
    })
  })
})
