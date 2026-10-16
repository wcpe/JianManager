/**
 * 「手动导入归档（.gz）」入口（FR-504）DOM 断言。
 *
 * 覆盖四条产品面红线：
 * - **健康态可达**：coverage 完整（横幅不渲染）时入口仍必须存在——补历史最典型的场景就是
 *   「采集一切正常、只是历史 .gz 没导」，入口挂在横幅内会在此态整体消失（review D-01）；
 * - 只在**选中单一节点**时出现（导入按节点下发，未选节点无法确定目标）；
 * - 只在**平台管理员**态出现（全节点范围导入后端仅管理员放行，否则是一个必然 403 的入口，D-05）；
 * - 理由是必填，成功后关窗，且请求打到正确端点并携带理由与范围。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import { ROLE_GROUP_ADMIN, ROLE_PLATFORM_ADMIN } from '@/lib/roles'
import LogsPage from './LogsPage'

const seedLogsPage = {
  items: [
    {
      id: 1,
      source: 'instance',
      level: 'info',
      instanceId: 1,
      instanceUuid: 'i-1',
      nodeId: 1,
      message: 'import-archives-seed-log-line',
      time: '2026-07-18T12:00:00Z',
    },
  ],
  total: 1,
  page: 1,
  pageSize: 100,
}

/**
 * 「一切正常」的联邦响应：coverage 完整 + 无 partial reason ⇒ 覆盖横幅不渲染。
 * 这正是 D-01 的目标场景（也是入口过去唯一缺失的那个态）。
 */
function healthyFederationBody() {
  return {
    ok: true,
    sourceTag: 'federated',
    available: true,
    items: seedLogsPage.items,
    quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
    coverage: { complete: true, partial_reasons: [], targets: [], enumeration_state: 'EXHAUSTED' },
  }
}

function stubBaseHandlers() {
  server.use(
    http.get(API('/nodes'), () =>
      HttpResponse.json([{ id: 1, name: 'node-one', status: 'online' }]),
    ),
    http.get(API('/instances'), () => HttpResponse.json([])),
    http.get(API('/logs'), () => HttpResponse.json(seedLogsPage)),
    http.get(API('/logs/federation'), () => HttpResponse.json(healthyFederationBody())),
    http.get(API('/logs/federation/stats'), () =>
      HttpResponse.json({ points: [], coverage: { complete: true, targets: [] } }),
    ),
    http.get(API('/logs/federation/facets'), () =>
      HttpResponse.json({ dimensions: [], coverage: { complete: true, targets: [] } }),
    ),
  )
}

/**
 * 通过节点选择器真的选中一个节点（`nodeId` 只由该 Select 的 onValueChange 驱动，
 * 没有 URL 初始化——从 URL 预置会让用例测成一条不可达路径）。
 *
 * jsdom 未实现 scrollIntoView，Radix Select 展开时会调用它；且 Radix Select 需用键盘激活展开。
 */
async function selectNode(label = 'node-one') {
  Element.prototype.scrollIntoView = vi.fn()
  const trigger = await screen.findByTestId('logs-node-select')
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  await userEvent.click(await screen.findByRole('option', { name: label }))
}

/** 让后续渲染处于已登录态，并显式设定角色（auth store 的 role 来自 JWT 声明）。 */
function loginAs(role: number) {
  loginMockUser()
  useAuthStore.setState({ role })
}

describe('LogsPage × 手动导入归档（FR-504）', () => {
  beforeEach(() => {
    stubBaseHandlers()
  })

  it('① 覆盖完整的健康态（无横幅）下入口依然可见，点击可打开对话框', async () => {
    loginAs(ROLE_PLATFORM_ADMIN)

    renderWithProviders(<LogsPage />)
    await selectNode()

    // 前置事实：健康态确实没有覆盖横幅 —— 否则本用例会退化成「在横幅里找到按钮」这种
    // 恰好通过的假绿（旧实现正是只在横幅内渲染，本断言把它钉死）。
    await waitFor(() =>
      expect(screen.queryByTestId('logs-coverage-banner')).not.toBeInTheDocument(),
    )

    const openBtn = await screen.findByTestId('logs-import-archives-open')
    await userEvent.click(openBtn)
    expect(await screen.findByTestId('logs-import-archives-dialog')).toBeInTheDocument()
    // 未填理由时确认按钮禁用（后端缺理由即拒，前端先拦以避免必然失败的往返）。
    expect(screen.getByTestId('logs-import-archives-confirm')).toBeDisabled()
  })

  it('② 未选中单一节点 → 不给入口（无法确定要导哪个节点）', async () => {
    loginAs(ROLE_PLATFORM_ADMIN)

    renderWithProviders(<LogsPage />)
    // 等首屏查询落地后再断言缺席（避免测成"还没渲染出来"）。
    await waitFor(() => expect(screen.getByText('import-archives-seed-log-line')).toBeInTheDocument())
    expect(screen.queryByTestId('logs-import-archives-open')).not.toBeInTheDocument()
  })

  it('③ 非平台管理员不给入口；同条件下管理员可见（D-05：不放出必然 403 的入口）', async () => {
    loginAs(ROLE_GROUP_ADMIN)

    const { unmount } = renderWithProviders(<LogsPage />)
    await selectNode()
    await waitFor(() => expect(screen.getByText('import-archives-seed-log-line')).toBeInTheDocument())
    // 非管理员：全节点范围导入后端仅平台管理员放行（authorizeImportAllSources），
    // 放出按钮只会让用户填完必填理由后吃到 403。
    expect(screen.queryByTestId('logs-import-archives-open')).not.toBeInTheDocument()
    unmount()

    loginAs(ROLE_PLATFORM_ADMIN)
    renderWithProviders(<LogsPage />)
    await selectNode()
    expect(await screen.findByTestId('logs-import-archives-open')).toBeInTheDocument()
  })

  it('④ 填写理由后可确认，请求打到正确端点并携带理由与范围（成功后关窗）', async () => {
    loginAs(ROLE_PLATFORM_ADMIN)

    let posted: { url: string; body: unknown } | null = null
    server.use(
      http.post(API('/nodes/1/log-runtime/ingest/import-archives'), async ({ request }) => {
        posted = { url: request.url, body: await request.json() }
        return HttpResponse.json({ state: 'triggered', message: '已触发归档导入扫描' })
      }),
    )

    renderWithProviders(<LogsPage />)
    await selectNode()
    await userEvent.click(await screen.findByTestId('logs-import-archives-open'))
    await screen.findByTestId('logs-import-archives-dialog')

    const confirm = screen.getByTestId('logs-import-archives-confirm')
    const reason = screen.getByTestId('logs-import-archives-reason')

    // 纯空白不算理由（后端同样会拒）。
    await userEvent.type(reason, '   ')
    expect(confirm).toBeDisabled()

    await userEvent.clear(reason)
    await userEvent.type(reason, '补采轮转归档')
    expect(confirm).toBeEnabled()

    await userEvent.click(confirm)
    await waitFor(() => expect(posted).not.toBeNull())
    expect(posted!.url).toContain('/nodes/1/log-runtime/ingest/import-archives')
    // 理由进请求体（CP 再经可信 metadata 下传给 Worker 作为审计凭据）；
    // 未指定单源 ⇒ 全节点范围（storageNamespace 为空串，后端仅允许平台管理员）。
    expect(posted!.body).toMatchObject({ reason: '补采轮转归档', storageNamespace: '' })

    await waitFor(() =>
      expect(screen.queryByTestId('logs-import-archives-dialog')).not.toBeInTheDocument(),
    )
  })
  it('⑤ 联邦查询降级（日志查询服务未启用）时入口仍可用——导入走 LogImportArchives，与查询面不是同一能力', async () => {
    // 生产实测组合（浏览器人工验收发现）：查询服务未启用 ⇒ 前端降级为 legacy、`federationActive=false`，
    // 而同一时刻 `POST /nodes/:id/log-runtime/ingest/import-archives` 返回 200 并成功导入。
    // 若把入口绑在查询可用性上，最需要补历史归档的降级场景反而没有按钮（本用例锁死该回归）。
    server.use(
      http.get(API('/logs/federation'), () => new HttpResponse(null, { status: 404 })),
      http.get(API('/logs/federation/stats'), () => new HttpResponse(null, { status: 404 })),
      http.get(API('/logs/federation/facets'), () => new HttpResponse(null, { status: 404 })),
    )
    loginAs(ROLE_PLATFORM_ADMIN)
    renderWithProviders(<LogsPage />)
    await selectNode()

    expect(await screen.findByTestId('logs-import-archives-open')).toBeInTheDocument()
  })

})
