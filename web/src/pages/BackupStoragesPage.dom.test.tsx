import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
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
