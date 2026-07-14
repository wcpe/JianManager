import { describe, it, expect, beforeEach, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
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
 * FR-338 编辑流：回显现值（含 ${VAR} 凭证引用）/type 禁用/PUT 保存刷新 + lastTest 重置/撞名 422/草稿测试连接。
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

  // ---- 编辑（FR-338）----

  it('编辑弹窗受控回显现值（含 ${VAR} 凭证引用原样）且 type 不可改', async () => {
    const user = userEvent.setup()
    renderWithProviders(<BackupStoragesPage />)
    const row = (await screen.findByText('s3-primary')).closest('tr') as HTMLElement

    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('编辑存储后端')).toBeInTheDocument()

    const [nameInput, endpointInput, bucketInput, regionInput, prefixInput, accessKeyInput, secretKeyInput] =
      within(dialog).getAllByRole('textbox')
    expect(nameInput).toHaveValue('s3-primary')
    expect(endpointInput).toHaveValue('s3.amazonaws.com')
    expect(bucketInput).toHaveValue('jm-backups')
    expect(regionInput).toHaveValue('us-east-1')
    expect(prefixInput).toHaveValue('prod/')
    // 凭证即 ${ENV_VAR} 引用（非明文），原样回显无泄露。
    expect(accessKeyInput).toHaveValue('${JIANMANAGER_BACKUP_S3_AK}')
    expect(secretKeyInput).toHaveValue('${JIANMANAGER_BACKUP_S3_SK}')
    // type 不可改（改型=删重建，后端 422 双保险）。
    expect(within(dialog).getByRole('button', { name: 'S3' })).toBeDisabled()
    // s3-primary 被 1 个备份引用 → 显示改指向风险提示条。
    expect(within(dialog).getByText(/已被备份引用/)).toBeInTheDocument()
  })

  it('编辑保存提交 PUT → 列表反映新值且最近测试列回未测试态', async () => {
    const user = userEvent.setup()
    renderWithProviders(<BackupStoragesPage />)
    const row = (await screen.findByText('s3-primary')).closest('tr') as HTMLElement
    // seed 带既往测试结论，编辑成功后应被清空（配置已变，旧结论失效）。
    expect(within(row).getByText('连接正常')).toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    const [nameInput, endpointInput] = within(dialog).getAllByRole('textbox')
    await user.clear(nameInput)
    await user.type(nameInput, 's3-renamed')
    await user.clear(endpointInput)
    await user.type(endpointInput, 'minio.internal:9000')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    // PUT 成功 → 关窗 + 列表刷新为新值（替换而非追加）。
    const renamed = await screen.findByText('s3-renamed')
    expect(screen.queryByText('s3-primary')).not.toBeInTheDocument()
    const newRow = renamed.closest('tr') as HTMLElement
    expect(within(newRow).getByText(/minio\.internal:9000/)).toBeInTheDocument()
    // lastTest* 已清空 → 最近测试列回到未测试态。
    expect(within(newRow).queryByText('连接正常')).not.toBeInTheDocument()
  })

  it('编辑撞其他后端名 → 422 错误 toast 展示后端 message 且不关窗', async () => {
    const user = userEvent.setup()
    const toastError = vi.spyOn(toast, 'error')
    renderWithProviders(<BackupStoragesPage />)
    const row = (await screen.findByText('s3-primary')).closest('tr') as HTMLElement

    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    const [nameInput] = within(dialog).getAllByRole('textbox')
    await user.clear(nameInput)
    await user.type(nameInput, 'sftp-offsite') // 与 seed #2 撞名（devmock PUT 排除自身预检）
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith(expect.stringContaining('存储后端名称已存在')),
    )
    // 失败不关窗，草稿保留可继续修改。
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('编辑草稿可先测试连接（body 同形，不落库）', async () => {
    const user = userEvent.setup()
    let draftBody: Record<string, unknown> | null = null
    server.use(
      http.post(API('/backup-storages/test'), async ({ request }) => {
        draftBody = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ ok: true, message: '编辑草稿连接正常', latencyMs: 5 })
      }),
    )
    renderWithProviders(<BackupStoragesPage />)
    const row = (await screen.findByText('s3-primary')).closest('tr') as HTMLElement

    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    const [, endpointInput] = within(dialog).getAllByRole('textbox')
    await user.clear(endpointInput)
    await user.type(endpointInput, 'minio.new:9000')
    await user.click(within(dialog).getByRole('button', { name: '测试连接' }))

    await waitFor(() => expect(draftBody?.endpoint).toBe('minio.new:9000'))
    expect(draftBody?.name).toBe('s3-primary')
    expect(await screen.findByText('编辑草稿连接正常')).toBeInTheDocument()
    // 仅测试草稿，未提交保存 → 列表原值不变。
    expect(screen.getByText('s3-primary')).toBeInTheDocument()
  })
})
