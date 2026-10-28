import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { toast } from 'sonner'
import ConfigFilesPage from './ConfigFilesPage'

// toast 用 spy 替身：真实 Toaster 不在本页渲染树里，直接断言调用参数更稳。
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() } }))

/**
 * 分层配置·配置文件列表页（FR-529）的 DOM 测试。
 *
 * 这些端点（`/config-files`）的假后端 handler 由本文件就地注册（`server.use`），
 * 而不是加进 `@jianmanager/devmock` 的域簇：交付/纳管两套页面正在同一份域簇里开发，
 * 共享包内新增 handler 会把两条工作流的改动缠在一起。
 */

const FILE_ROW = {
  id: 7,
  name: 'plugins/Foo/config.yml',
  format: 'yaml',
  description: 'Foo 插件配置',
  schemaJson: '',
  sensitivePaths: '',
  createdBy: 1,
  createdAt: '2026-10-01T08:00:00Z',
  updatedAt: '2026-10-02T09:30:00Z',
  layerCount: 3,
}

/** 默认铺满成功响应：列表一条 + 其余端点空实现。 */
function useDefaultHandlers() {
  server.use(
    http.get(API('/config-files'), () =>
      HttpResponse.json({ files: [FILE_ROW], total: 1, page: 1, pageSize: 20 }),
    ),
  )
}

function renderPage() {
  loginMockUser()
  const user = userEvent.setup()
  renderWithProviders(<ConfigFilesPage />)
  return { user }
}

describe('ConfigFilesPage（FR-529 配置文件列表）', () => {
  it('渲染列表：名称、格式、层数、更新时间', async () => {
    useDefaultHandlers()
    renderPage()

    expect(await screen.findByText('plugins/Foo/config.yml')).toBeInTheDocument()
    expect(screen.getByText('yaml')).toBeInTheDocument()
    const row = screen.getByTestId('config-file-row-7')
    expect(within(row).getByText('3')).toBeInTheDocument()
    expect(screen.getByText('Foo 插件配置')).toBeInTheDocument()
  })

  it('空列表渲染空状态与新建引导', async () => {
    server.use(
      http.get(API('/config-files'), () => HttpResponse.json({ files: [], total: 0, page: 1, pageSize: 20 })),
    )
    renderPage()

    expect(await screen.findByText('尚无配置文件')).toBeInTheDocument()
    // 空态里要给下一步动作，否则用户不知道从哪开始（页头也有同文案按钮，故用 testid 定位空态那一个）。
    const emptyState = screen.getByText('尚无配置文件').closest('[data-slot="empty-state"]')
    expect(emptyState).not.toBeNull()
    expect(within(emptyState as HTMLElement).getByRole('button', { name: '新建配置文件' })).toBeInTheDocument()
  })

  it('取数失败渲染错误态，而不是「尚无配置文件」', async () => {
    server.use(
      http.get(API('/config-files'), () =>
        HttpResponse.json({ error: 'INTERNAL', message: '数据库不可用' }, { status: 500 }),
      ),
    )
    renderPage()

    expect(await screen.findByTestId('config-file-error')).toHaveTextContent('数据库不可用')
    // 把故障读成「还没建过配置」会让运维以为平台是空的，两者必须区分。
    expect(screen.queryByText('尚无配置文件')).toBeNull()
  })

  it('搜索无结果时提示与「尚无配置文件」不同', async () => {
    server.use(
      http.get(API('/config-files'), ({ request }) => {
        const q = new URL(request.url).searchParams.get('q')
        const files = q ? [] : [FILE_ROW]
        return HttpResponse.json({ files, total: files.length, page: 1, pageSize: 20 })
      }),
    )
    const { user } = renderPage()
    await screen.findByText('plugins/Foo/config.yml')

    await user.type(screen.getByTestId('config-file-search'), 'nope')
    expect(await screen.findByText('没有匹配的配置文件')).toBeInTheDocument()
    expect(screen.queryByText('尚无配置文件')).toBeNull()
  })

  it('新建：提交后用返回数据出现在列表（名称与格式创建后不可改）', async () => {
    const created = { ...FILE_ROW, id: 8, name: 'server.properties', format: 'properties', description: '' }
    let files = [FILE_ROW]
    server.use(
      http.get(API('/config-files'), () => HttpResponse.json({ files, total: files.length, page: 1, pageSize: 20 })),
      http.post(API('/config-files'), () => {
        files = [...files, created]
        return HttpResponse.json(created)
      }),
    )
    const { user } = renderPage()
    await screen.findByText('plugins/Foo/config.yml')

    await user.click(screen.getByTestId('config-file-create'))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('名称'), 'server.properties')
    await user.selectOptions(within(dialog).getByLabelText('格式'), 'properties')
    await user.click(screen.getByTestId('config-file-submit'))

    expect(await screen.findByText('server.properties')).toBeInTheDocument()
  })

  it('编辑既有配置时名称与格式被锁定（后端会以不可改拒绝）', async () => {
    useDefaultHandlers()
    const { user } = renderPage()
    await screen.findByText('plugins/Foo/config.yml')

    await user.click(screen.getByTestId('config-file-edit-7'))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('名称')).toBeDisabled()
    expect(within(dialog).getByLabelText('格式')).toBeDisabled()
  })

  it('删除走二次确认，确认后从列表消失', async () => {
    let files = [FILE_ROW]
    server.use(
      http.get(API('/config-files'), () => HttpResponse.json({ files, total: files.length, page: 1, pageSize: 20 })),
      http.delete(API('/config-files/7'), () => {
        files = []
        return HttpResponse.json({ deleted: true })
      }),
    )
    const { user } = renderPage()
    await screen.findByText('plugins/Foo/config.yml')

    await user.click(screen.getByTestId('config-file-delete-7'))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '删除' }))

    await waitFor(() => expect(screen.queryByText('plugins/Foo/config.yml')).toBeNull())
    expect(await screen.findByText('尚无配置文件')).toBeInTheDocument()
  })

  it('后端错误码的定点文案原样透出（同名冲突）', async () => {
    server.use(
      http.get(API('/config-files'), () => HttpResponse.json({ files: [FILE_ROW], total: 1, page: 1, pageSize: 20 })),
      http.post(API('/config-files'), () =>
        HttpResponse.json({ error: 'ALREADY_EXISTS', message: '同名配置文件已存在' }, { status: 409 }),
      ),
    )
    const { user } = renderPage()
    await screen.findByText('plugins/Foo/config.yml')

    await user.click(screen.getByTestId('config-file-create'))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('名称'), 'dup.yml')
    await user.click(screen.getByTestId('config-file-submit'))

    // toast 文案取后端 message，不用通用「保存失败」盖掉更具体的原因。
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('同名配置文件已存在'))
  })
})

/**
 * 清空元数据字段必须真的把字段发给后端。
 *
 * 回归背景：后端用三态语义「字段缺席=不改、显式空串=清空」，页面若按真值条件展开
 * （`...(schemaJson ? { schemaJson } : {})`），空值会被省略 → 用户永远清不掉该字段，
 * 且接口返回 200，看不出任何异常。
 */
describe('清空 description / schemaJson', () => {
  it('编辑时把空串字段原样发出，不被条件展开省略', async () => {
    const WITH_SCHEMA = { ...FILE_ROW, description: '有描述', schemaJson: '{"type":"object"}' }
    let putBody: Record<string, unknown> | null = null

    server.use(
      http.get(API('/config-files'), () =>
        HttpResponse.json({ files: [WITH_SCHEMA], total: 1, page: 1, pageSize: 20 }),
      ),
      http.put(API('/config-files/:id'), async ({ request }) => {
        putBody = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ ...WITH_SCHEMA, description: '', schemaJson: '' })
      }),
    )

    const { user } = renderPage()
    await screen.findByText('plugins/Foo/config.yml')

    await user.click(screen.getByTestId('config-file-edit-7'))
    const dialog = await screen.findByRole('dialog')

    // 清空两个字段。
    const descInput = within(dialog).getByLabelText('描述')
    await user.clear(descInput)
    const schemaInput = within(dialog).getByLabelText('JSON Schema（可选）')
    await user.clear(schemaInput)

    await user.click(screen.getByTestId('config-file-submit'))

    await waitFor(() => expect(putBody).not.toBeNull())
    // 关键断言：两个键都必须存在且为空串。
    expect(putBody).toHaveProperty('description', '')
    expect(putBody).toHaveProperty('schemaJson', '')
  })
})
