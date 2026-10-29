import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { Routes, Route } from 'react-router'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ConfigFileDetailPage from './ConfigFileDetailPage'

/** 
 * 分层配置·详情页的 DOM 测试。
 *
 * 重点覆盖两条真机验收提的 UI 约束（spec §7.4 / §7.5）：
 * 1. 预览必须明示「规范化结果、别复制回磁盘」——且是**常驻**提示，不是有数据才出现；
 * 2. 「实例层优先级最高」提示的显示条件——必须同时有分组层与实例层，
 *    且所选实例**自己**有实例层；别的实例有实例层不能触发。
 */

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() } }))

const FILE = {
  id: 7,
  name: 'server.properties',
  format: 'properties',
  description: '',
  schemaJson: '',
  sensitivePaths: '',
  createdBy: 1,
  createdAt: '2026-10-01T08:00:00Z',
  updatedAt: '2026-10-02T09:30:00Z',
  layerCount: 2,
}

/** 一条层 head（只填本页用到的字段）。 */
function layer(scopeLevel: 'group' | 'instance', scopeRefId: number, scopeName: string, versionNo = 1) {
  return {
    scopeLevel,
    scopeRefId,
    scopeName,
    versionId: scopeRefId * 100 + versionNo,
    versionNo,
    contentHash: 'a'.repeat(64),
    remark: '',
    createdBy: 1,
    createdAt: 1759300000,
    contentSize: 20,
  }
}

/** 有效值预览（`server-port` 来自实例层，`motd` 来自分组层）。 */
const EFFECTIVE = {
  instanceId: 1,
  configFileId: 7,
  format: 'properties',
  content: 'motd=cluster\nserver-port=25566\n',
  effectiveHash: 'b'.repeat(64),
  traced: {
    'server-port': { value: 25566, source: { level: 'instance', id: 1, name: 'survival-1', versionNo: 1 } },
    motd: { value: 'cluster', source: { level: 'group', id: 2, name: '生存', versionNo: 3 } },
    debug: { value: null, source: { level: 'group', id: 2, name: '生存', versionNo: 3 }, deleted: true },
  },
  warnings: [],
  schemaIssues: [],
  layers: [],
}

/** 铺好本页所需的四个只读端点（文件/层/历史/预览）。 */
function useHandlers(layers: ReturnType<typeof layer>[], effective: unknown = EFFECTIVE) {
  server.use(
    http.get(API('/config-files/7'), () => HttpResponse.json(FILE)),
    http.get(API('/config-files/7/layers'), () => HttpResponse.json({ layers })),
    http.get(API('/config-files/7/layers/:scopeType/:scopeId/history'), ({ params }) =>
      HttpResponse.json({
        versions: [
          {
            id: 1,
            versionNo: 3,
            contentHash: 'c'.repeat(64),
            content: `# 来自 ${String(params.scopeType)}:${String(params.scopeId)}\nserver-port=25566\n`,
            remark: '调整端口',
            createdBy: 1,
            createdAt: 1759300000,
          },
        ],
      }),
    ),
    http.get(API('/config-files/7/effective'), () => HttpResponse.json(effective)),
  )
}

function renderPage() {
  loginMockUser()
  const user = userEvent.setup()
  renderWithProviders(
    <Routes>
      <Route path="/config-files/:id" element={<ConfigFileDetailPage />} />
    </Routes>,
    { route: '/config-files/7' },
  )
  return { user }
}

/** 
 * 在预览面板里选中实例「survival-1」。
 *
 * 先等预览面板出现（它只在文件取数成功后挂载），否则点击会落在骨架屏上。
 * Combobox 的候选项是 `<button>`（不是 role=option），故按可见名匹配。
 */
async function pickPreviewInstance(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByTestId('config-preview-normalized-notice')
  await user.click(screen.getByLabelText('预览实例'))
  // 候选窗口是服务端前 50 条（种子实例数远大于此），故先在下拉搜索框里键入再点。
  const search = await screen.findByPlaceholderText('搜索或输入…')
  await user.type(search, 'survival-1')
  await user.click(await screen.findByRole('button', { name: 'survival-1' }))
}

describe('ConfigFileDetailPage（层版本与有效值预览）', () => {
  it('左栏按作用域分组列出层，各自显示 head 版本号', async () => {
    useHandlers([layer('group', 2, '生存', 3), layer('instance', 1, 'survival-1', 1)])
    renderPage()

    expect(await screen.findByText('分组层')).toBeInTheDocument()
    expect(screen.getByText('实例层')).toBeInTheDocument()
    expect(screen.getByTestId('config-layer-item-group-2')).toHaveTextContent('生存')
    expect(screen.getByTestId('config-layer-item-group-2')).toHaveTextContent('v3')
    expect(screen.getByTestId('config-layer-item-instance-1')).toHaveTextContent('v1')
  })

  it('没有层时渲染空态并提示如何开始', async () => {
    useHandlers([])
    renderPage()

    expect(await screen.findByText('还没有任何层版本')).toBeInTheDocument()
    expect(screen.getByText('点右上角 + 为某个分组或实例建立第一份覆盖。')).toBeInTheDocument()
  })

  it('配置文件取数失败时渲染错误态（而不是空页面）', async () => {
    server.use(
      http.get(API('/config-files/7'), () =>
        HttpResponse.json({ error: 'NOT_FOUND', message: '配置文件不存在' }, { status: 404 }),
      ),
      http.get(API('/config-files/7/layers'), () => HttpResponse.json({ layers: [] })),
    )
    renderPage()

    expect(await screen.findByTestId('config-file-detail-error')).toHaveTextContent('配置文件不存在')
  })

  it('层列表取数失败时左栏不误报「还没有任何层」', async () => {
    server.use(
      http.get(API('/config-files/7'), () => HttpResponse.json(FILE)),
      http.get(API('/config-files/7/layers'), () =>
        HttpResponse.json({ error: 'BUSINESS_ERROR', message: '读取层失败' }, { status: 422 }),
      ),
    )
    renderPage()

    // 故障必须与「真的没有层」区分开，否则运维会去建一个已存在的层。
    expect(await screen.findByTestId('config-layers-error')).toHaveTextContent('读取层失败')
    expect(screen.queryByText('还没有任何层版本')).toBeNull()
  })

  it('选中层后编辑器载入该层 head 原文，并列出历史版本', async () => {
    useHandlers([layer('group', 2, '生存', 3)])
    const { user } = renderPage()

    await user.click(await screen.findByTestId('config-layer-item-group-2'))
    const editor = (await screen.findByTestId('config-layer-editor')) as HTMLTextAreaElement
    await waitFor(() => expect(editor.value).toContain('server-port=25566'))
    expect(screen.getByTestId('config-layer-history')).toBeInTheDocument()
    expect(screen.getByText('调整端口')).toBeInTheDocument()
  })

  // ---- 约束 1：预览是规范化结果 ----

  it('约束 1：未选实例时规范化提示就已常驻（用户点预览前就该知道）', async () => {
    useHandlers([layer('group', 2, '生存', 3)])
    renderPage()

    const notice = await screen.findByTestId('config-preview-normalized-notice')
    expect(notice).toHaveTextContent('预览的是规范化结果，不是磁盘原文')
    expect(notice).toHaveTextContent('不保留原始注释、缩进与键顺序')
    expect(notice).toHaveTextContent('请勿直接复制回磁盘')
    // 同时在场的还有「尚未选择实例」的空态，二者不互斥。
    expect(screen.getByText('尚未选择实例')).toBeInTheDocument()
  })

  it('约束 1：选中实例后提示仍在（不是一次性提示）', async () => {
    useHandlers([layer('group', 2, '生存', 3)])
    const { user } = renderPage()

    await screen.findByTestId('config-preview-normalized-notice')
    await pickPreviewInstance(user)

    expect(await screen.findByTestId('config-preview-content')).toBeInTheDocument()
    expect(screen.getByTestId('config-preview-normalized-notice')).toBeInTheDocument()
  })

  // ---- 约束 2：实例层优先级最高 ----

  it('约束 2：同时有分组层与该实例的实例层 → 显示优先级提示', async () => {
    useHandlers([layer('group', 2, '生存', 3), layer('instance', 1, 'survival-1', 1)])
    const { user } = renderPage()

    // 未选实例时不提示（还没有「哪层生效」的问题）。
    expect(screen.queryByTestId('config-preview-instance-precedence')).toBeNull()

    await pickPreviewInstance(user)

    const warn = await screen.findByTestId('config-preview-instance-precedence')
    expect(warn).toHaveTextContent('实例层会压住分组层')
    expect(warn).toHaveTextContent('实例层优先级最高，分组层覆盖不了它')
    expect(warn).toHaveTextContent('需先删除该实例的层版本，或直接改实例层')
  })

  it('约束 2：只有分组层（该实例没有实例层）→ 不提示', async () => {
    useHandlers([layer('group', 2, '生存', 3)])
    const { user } = renderPage()

    await pickPreviewInstance(user)
    await screen.findByTestId('config-preview-content')

    expect(screen.queryByTestId('config-preview-instance-precedence')).toBeNull()
  })

  it('约束 2：只有实例层、没有分组层 → 不提示', async () => {
    useHandlers([layer('instance', 1, 'survival-1', 1)])
    const { user } = renderPage()

    await pickPreviewInstance(user)
    await screen.findByTestId('config-preview-content')

    expect(screen.queryByTestId('config-preview-instance-precedence')).toBeNull()
  })

  it('约束 2：分组层 + 别的实例的实例层 → 不提示（与本实例无关）', async () => {
    // 实例 2（lobby-proxy）有实例层，但预览选的是实例 1：这是最容易被写错的判据。
    useHandlers([layer('group', 2, '生存', 3), layer('instance', 2, 'lobby-proxy', 1)])
    const { user } = renderPage()

    await pickPreviewInstance(user)
    await screen.findByTestId('config-preview-content')

    expect(screen.queryByTestId('config-preview-instance-precedence')).toBeNull()
  })

  // ---- traced 来源表 ----

  it('traced 来源表渲染键/值/来源层/版本，被删除的键标注', async () => {
    useHandlers([layer('group', 2, '生存', 3), layer('instance', 1, 'survival-1', 1)])
    const { user } = renderPage()

    await pickPreviewInstance(user)
    const table = await screen.findByTestId('config-traced-table')

    const portRow = within(table).getByTestId('config-traced-row-server-port')
    expect(portRow).toHaveTextContent('server-port')
    expect(portRow).toHaveTextContent('25566')
    expect(portRow).toHaveTextContent('实例层 · survival-1')
    expect(portRow).toHaveTextContent('v1')

    const motdRow = within(table).getByTestId('config-traced-row-motd')
    expect(motdRow).toHaveTextContent('cluster')
    expect(motdRow).toHaveTextContent('分组层 · 生存')
    expect(motdRow).toHaveTextContent('v3')

    // 被 null 删除的键：值列显示「已删除」而不是字面 null。
    const debugRow = within(table).getByTestId('config-traced-row-debug')
    expect(debugRow).toHaveTextContent('（已删除）')
  })

  it('合并结果原样展示（规范化文本，不做再加工）', async () => {
    useHandlers([layer('group', 2, '生存', 3)])
    const { user } = renderPage()

    await pickPreviewInstance(user)
    const content = await screen.findByTestId('config-preview-content')
    expect(content.textContent).toBe('motd=cluster\nserver-port=25566\n')
    // hash 前缀也给出，便于与磁盘 sha256 对账。
    expect(screen.getByText('bbbbbbbbbbbb')).toBeInTheDocument()
  })

  it('预览接口失败时渲染错误态（不静默显示空结果）', async () => {
    server.use(
      http.get(API('/config-files/7'), () => HttpResponse.json(FILE)),
      http.get(API('/config-files/7/layers'), () => HttpResponse.json({ layers: [layer('group', 2, '生存', 3)] })),
      http.get(API('/config-files/7/effective'), () =>
        HttpResponse.json({ error: 'BUSINESS_ERROR', message: '层原文解析失败' }, { status: 422 }),
      ),
    )
    const { user } = renderPage()

    await pickPreviewInstance(user)
    expect(await screen.findByTestId('config-preview-error')).toHaveTextContent('层原文解析失败')
    expect(screen.queryByTestId('config-preview-content')).toBeNull()
  })

  it('合并告警逐条展示（语法错误不能被吞掉）', async () => {
    useHandlers([layer('group', 2, '生存', 3)], {
      ...EFFECTIVE,
      warnings: [
        {
          level: 'error',
          code: 'LAYER_PARSE_FAILED',
          message: '生存 层第 3 行解析失败',
          source: { level: 'group', id: 2, name: '生存', versionNo: 3 },
        },
      ],
    })
    const { user } = renderPage()

    await pickPreviewInstance(user)
    const box = await screen.findByTestId('config-preview-warnings')
    expect(box).toHaveTextContent('生存 层第 3 行解析失败')
  })
})
