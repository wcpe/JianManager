import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ConfigAdoptPage from './ConfigAdoptPage'

/**
 * 配置纳管页（FR-533）DOM 测试。
 *
 * 重点盯三件容易做错的事：
 * 1. **执行必须先预演**：纳管是不可撤销的写入且已纳管不覆盖，误纳管只能人工清理；
 * 2. **报告分类不合并**：已纳管（ALREADY_ADOPTED）是「跳过」不是「失败」，
 *    解析告警是「告警」不是「失败」——三者混在一起会让运维以为纳管大面积失败了；
 * 3. **预演端点真的没写库**：预演只打 `/adopt/preview`，执行只打 `/adopt`。
 */

/** 假后端的发现结果：一个可纳管 + 一个格式不支持。 */
const DISCOVER_BODY = {
  files: [
    { path: 'server.properties', format: 'properties', supported: true },
    { path: 'plugins/Foo/config.yml', format: 'yaml', supported: true },
    { path: 'crash-reports/dump.toml', format: 'toml', supported: false },
  ],
  truncated: false,
}

/** 构造一份纳管响应：一条新建 + 一条已纳管 + 一条解析告警。 */
function adoptBody(dryRun: boolean) {
  return {
    results: [
      {
        instanceId: 1,
        instanceName: '生存服',
        path: 'server.properties',
        name: 'server.properties',
        format: 'properties',
        size: 42,
        contentHash: 'a'.repeat(64),
        configFileId: 10,
        versionId: 20,
        versionNo: 1,
        status: 'created',
        action: 'create_file_and_baseline',
        code: 'ADOPTED',
        dryRun,
      },
      {
        instanceId: 1,
        instanceName: '生存服',
        path: 'plugins/Foo/config.yml',
        name: 'plugins/Foo/config.yml',
        format: 'yaml',
        size: 7,
        status: 'already_adopted',
        code: 'ALREADY_ADOPTED',
        message: '该实例上已纳管，跳过且不覆盖',
        dryRun,
      },
      {
        instanceId: 1,
        instanceName: '生存服',
        path: 'plugins/Bar/broken.yml',
        name: 'plugins/Bar/broken.yml',
        format: 'yaml',
        size: 11,
        contentHash: 'b'.repeat(64),
        status: 'created',
        code: 'ADOPTED',
        // 解析失败只作为 warning 带出：纳管照常记录现状。
        warning: 'YAML 第 2 行缩进非法',
        dryRun,
      },
    ],
    summary: {
      total: 3,
      created: 2,
      appended: 0,
      unchanged: 0,
      alreadyAdopted: 1,
      failed: 0,
      dryRun,
    },
    dryRun,
  }
}

/** 渲染纳管页：先登录，再注册发现/纳管两个端点的响应。 */
function renderPage(overrides: { discover?: typeof DISCOVER_BODY } = {}) {
  loginMockUser()
  server.use(
    http.get(API('/instances/:id/configs/discover'), () =>
      HttpResponse.json(overrides.discover ?? DISCOVER_BODY),
    ),
    http.get(API('/config-files/health'), () =>
      HttpResponse.json({
        configFileCount: 42,
        layerVersionCount: 512,
        lastWriteAt: '2026-10-23T12:00:00Z',
        notice: '若 Beacon 侧配置功能仍开启，请确认不会两边同时修改同一份配置。JM 无法自动检测该状态。',
      }),
    ),
  )
  const user = userEvent.setup()
  renderWithProviders(<ConfigAdoptPage />)
  return { user }
}

/** 选中第一个实例（假后端实例列表由 devmock 播种）。 */
async function pickFirstInstance(user: ReturnType<typeof userEvent.setup>) {
  const select = await screen.findByTestId('adopt-instance-select')
  await waitFor(() => {
    expect(within(select).getAllByRole('option').length).toBeGreaterThan(1)
  })
  const option = within(select).getAllByRole('option')[1]
  await user.selectOptions(select, option)
}

describe('ConfigAdoptPage（FR-533 配置纳管）', () => {
  it('把「只读磁盘 / 不覆盖 / 坏的也记录」三条语义摆在页面上', async () => {
    renderPage()
    const semantics = await screen.findByTestId('adopt-semantics')
    // 三条都是会让人误解纳管的东西，缺一条就会有人把它当「导入」来用。
    expect(semantics.textContent).toContain('mtime 不变')
    expect(semantics.textContent).toContain('ALREADY_ADOPTED')
    expect(semantics.textContent).toContain('告警')
  })

  it('扫描后只有受支持格式可勾选，.toml 明确标注格式不支持', async () => {
    const { user } = renderPage()
    await pickFirstInstance(user)

    const rows = await screen.findAllByTestId('adopt-candidate-row')
    expect(rows.length).toBe(3)
    // .toml 不在 config_file 支持的三种格式里：直接禁勾，省掉一次注定失败的往返。
    const tomlRow = rows.find((r) => r.textContent?.includes('dump.toml'))
    expect(tomlRow).toBeDefined()
    expect(within(tomlRow as HTMLElement).getByRole('checkbox')).toBeDisabled()
    expect(within(tomlRow as HTMLElement).getByText('格式不支持')).toBeInTheDocument()
  })

  it('预演 → 执行：执行按钮在预演前禁用，执行只打 /adopt 不打 /adopt/preview', async () => {
    const calls: string[] = []
    server.use(
      http.post(API('/config-files/adopt/preview'), async () => {
        calls.push('preview')
        return HttpResponse.json(adoptBody(true))
      }),
      http.post(API('/config-files/adopt'), async () => {
        calls.push('adopt')
        return HttpResponse.json(adoptBody(false))
      }),
    )
    const { user } = renderPage()
    await pickFirstInstance(user)

    const rows = await screen.findAllByTestId('adopt-candidate-row')
    const serverRow = rows.find((r) => r.textContent?.includes('server.properties')) as HTMLElement
    await user.click(within(serverRow).getByRole('checkbox'))

    const execute = screen.getByTestId('adopt-execute')
    // 未预演时执行禁用：纳管不可撤销，先让人看清将要写什么。
    expect(execute).toBeDisabled()
    expect(screen.getByTestId('adopt-need-preview')).toBeInTheDocument()

    await user.click(screen.getByTestId('adopt-preview'))
    expect(await screen.findByTestId('adopt-report')).toBeInTheDocument()
    expect(calls).toEqual(['preview'])

    await waitFor(() => expect(screen.getByTestId('adopt-execute')).toBeEnabled())
    await user.click(screen.getByTestId('adopt-execute'))
    await waitFor(() => expect(calls).toEqual(['preview', 'adopt']))
  })

  it('报告把「已纳管」记为跳过、「解析告警」记为告警，都不算失败', async () => {
    server.use(
      http.post(API('/config-files/adopt/preview'), () => HttpResponse.json(adoptBody(true))),
    )
    const { user } = renderPage()
    await pickFirstInstance(user)

    const rows = await screen.findAllByTestId('adopt-candidate-row')
    const serverRow = rows.find((r) => r.textContent?.includes('server.properties')) as HTMLElement
    await user.click(within(serverRow).getByRole('checkbox'))
    await user.click(screen.getByTestId('adopt-preview'))

    const report = await screen.findByTestId('adopt-report')
    // 成功 2（含一条带告警）/ 跳过 1 / 失败 0：三类分开计数。
    expect(within(report).getByText('成功 1')).toBeInTheDocument()
    expect(within(report).getByText('告警 1')).toBeInTheDocument()
    expect(within(report).getByText('跳过 1')).toBeInTheDocument()
    expect(within(report).getByText('失败 0')).toBeInTheDocument()

    // 已纳管行的状态文案必须说「已纳管（跳过）」，不能显示成失败。
    const resultRows = within(report).getAllByTestId('adopt-result-row')
    const alreadyRow = resultRows.find((r) => r.textContent?.includes('plugins/Foo/config.yml')) as HTMLElement
    expect(within(alreadyRow).getByText('已纳管（跳过）')).toBeInTheDocument()

    // 解析告警单独提示，且该行状态仍是「已新建」而非失败。
    const brokenRow = resultRows.find((r) => r.textContent?.includes('broken.yml')) as HTMLElement
    expect(within(brokenRow).getByText('已新建')).toBeInTheDocument()
    expect(within(brokenRow).getByText(/解析告警/)).toBeInTheDocument()
  })

  it('预演报告标注「未写入」，执行后不再带该标记', async () => {
    server.use(
      http.post(API('/config-files/adopt/preview'), () => HttpResponse.json(adoptBody(true))),
      http.post(API('/config-files/adopt'), () => HttpResponse.json(adoptBody(false))),
    )
    const { user } = renderPage()
    await pickFirstInstance(user)

    const rows = await screen.findAllByTestId('adopt-candidate-row')
    const serverRow = rows.find((r) => r.textContent?.includes('server.properties')) as HTMLElement
    await user.click(within(serverRow).getByRole('checkbox'))

    await user.click(screen.getByTestId('adopt-preview'))
    expect(await screen.findByText('预演 · 未写入')).toBeInTheDocument()
    expect(screen.getByText('预演报告（未写入任何数据）')).toBeInTheDocument()

    await user.click(screen.getByTestId('adopt-execute'))
    expect(await screen.findByText('执行结果')).toBeInTheDocument()
    expect(screen.queryByText('预演 · 未写入')).toBeNull()
  })

  it('明示实例层优先级最高、分组层覆盖不了它', async () => {
    renderPage()
    // 这条不写清，纳管 100 台后运维会困惑「为什么改了分组层没反应」。
    const tip = await screen.findByTestId('adopt-instance-precedence')
    expect(tip.textContent).toContain('实例层优先级最高')
    expect(tip.textContent).toContain('分组层覆盖不了')
  })

  it('健康自检展示配置数 / 层版本数 / 最近写入，并原样转述人工核对提示', async () => {
    renderPage()
    const health = await screen.findByTestId('adopt-health')
    // 自检面板先以占位符渲染、取数完成后才出数值，故等数值出现而不是只等容器。
    await waitFor(() => expect(within(health).getByText('42')).toBeInTheDocument())
    expect(within(health).getByText('512')).toBeInTheDocument()
    expect(within(health).getByText('2026-10-23T12:00:00Z')).toBeInTheDocument()

    const notice = await screen.findByTestId('adopt-health-notice')
    // 后端明确「无法自动检测」，前端不能把它改写成告警或结论。
    expect(notice.textContent).toContain('JM 无法自动检测该状态')
  })
})
