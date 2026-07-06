import { describe, it, expect, beforeEach } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import BackupStoragesPage from './BackupStoragesPage'

// 新增对话框内的 Combobox（Radix Popover）依赖 ResizeObserver，jsdom 未实现，需垫片。
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

/**
 * 备份存储后端页（FR-207 域簇）。三条强断言：
 * ① 渲染出 seed 存储后端；② 新建后列表联动出现新行；③ 注入 500 → 显空态（不崩溃）。
 * 表单字段标签未与 input 关联（FieldLabel 无 htmlFor），故按 DOM 顺序 / placeholder 选取。
 */
describe('BackupStoragesPage（mock）', () => {
  beforeEach(() => {
    loginMockUser() // 受 requireAuth 保护，渲染前置已登录态
  })

  it('渲染 seed 存储后端', async () => {
    renderWithProviders(<BackupStoragesPage />)
    expect(await screen.findByText('s3-primary')).toBeInTheDocument()
    expect(screen.getByText('sftp-offsite')).toBeInTheDocument()
    // 凭证以 ${ENV_VAR} 引用展示，不返回明文（FR-057）。
    expect(screen.getByText('${JIANMANAGER_BACKUP_S3_AK}')).toBeInTheDocument()
    expect(screen.getByText('256 MB · 1 个备份')).toBeInTheDocument()
    expect(screen.getByText('连接正常')).toBeInTheDocument()
  })

  it('新建存储后端 → 列表联动出现新行', async () => {
    const user = userEvent.setup()
    renderWithProviders(<BackupStoragesPage />)
    await screen.findByText('s3-primary')

    await user.click(screen.getByRole('button', { name: '新增存储后端' }))
    const dialog = await screen.findByRole('dialog')

    // 仅名称必填（validateRequired）；凭证为 ${ENV_VAR} 形式，空串亦合法，故只填名称即可提交。
    // 名称是表单第一个文本输入框（FieldLabel 无 htmlFor，无法按标签定位）。
    const [nameInput] = within(dialog).getAllByRole('textbox')
    await user.type(nameInput, 'minio-dev')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    expect(await screen.findByText('minio-dev')).toBeInTheDocument()
    // seed 行仍在，确认是追加而非替换。
    expect(screen.getByText('s3-primary')).toBeInTheDocument()
  })

  it('未保存表单可先测试连接且不创建存储后端', async () => {
    const user = userEvent.setup()
    let draftBody: Record<string, unknown> | null = null
    server.use(
      http.post(API('/backup-storages/test'), async ({ request }) => {
        draftBody = await request.json() as Record<string, unknown>
        return HttpResponse.json({ ok: true, message: '草稿连接正常', latencyMs: 8 })
      }),
    )
    renderWithProviders(<BackupStoragesPage />)
    await screen.findByText('s3-primary')

    await user.click(screen.getByRole('button', { name: '新增存储后端' }))
    const dialog = await screen.findByRole('dialog')
    const [nameInput, endpointInput, bucketInput, regionInput, prefixInput, accessKeyInput, secretKeyInput] =
      within(dialog).getAllByRole('textbox')
    await user.type(nameInput, 'draft-minio')
    await user.type(endpointInput, 'minio.local:9000')
    await user.type(bucketInput, 'backups')
    await user.type(regionInput, 'us-east-1')
    await user.type(prefixInput, 'draft/')
    fireEvent.change(accessKeyInput, { target: { value: '${JIANMANAGER_BACKUP_S3_AK}' } })
    fireEvent.change(secretKeyInput, { target: { value: '${JIANMANAGER_BACKUP_S3_SK}' } })

    await user.click(within(dialog).getByRole('button', { name: '测试连接' }))

    await waitFor(() => expect(draftBody?.name).toBe('draft-minio'))
    expect(await screen.findByText('草稿连接正常')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.queryByRole('cell', { name: 'draft-minio' })).not.toBeInTheDocument()
  })

  it('未保存表单测试连接失败时展示原因', async () => {
    const user = userEvent.setup()
    server.use(
      http.post(API('/backup-storages/test'), () =>
        HttpResponse.json({ ok: false, message: '凭证环境变量未设置：JM_MISSING_SK', latencyMs: 3 }),
      ),
    )
    renderWithProviders(<BackupStoragesPage />)
    await screen.findByText('s3-primary')

    await user.click(screen.getByRole('button', { name: '新增存储后端' }))
    const dialog = await screen.findByRole('dialog')
    const [nameInput] = within(dialog).getAllByRole('textbox')
    await user.type(nameInput, 'broken-storage')
    await user.click(within(dialog).getByRole('button', { name: '测试连接' }))

    expect(await screen.findByText('凭证环境变量未设置：JM_MISSING_SK')).toBeInTheDocument()
    expect(screen.queryByRole('cell', { name: 'broken-storage' })).not.toBeInTheDocument()
  })

  it('测试连接后回写最近测试状态', async () => {
    const user = userEvent.setup()
    renderWithProviders(<BackupStoragesPage />)
    const row = (await screen.findByText('sftp-offsite')).closest('tr') as HTMLElement

    await user.click(within(row).getByRole('button', { name: '测试' }))

    await waitFor(() => expect(within(row).getByText('连接正常')).toBeInTheDocument())
  })

  it('注入 500 → 显示空态而非崩溃', async () => {
    mockInject('get', '/backup-storages', { kind: 'status', status: 500 })
    renderWithProviders(<BackupStoragesPage />)
    // 列表查询失败 → storages 为 undefined → 渲染空态文案，页面不崩。
    expect(await screen.findByText('暂无存储后端，备份默认存于节点本地')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('s3-primary')).not.toBeInTheDocument()
    })
  })
})
