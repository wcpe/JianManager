import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import ArtifactStoragesPage from './ArtifactStoragesPage'

// 对话框内 Radix 组件（Checkbox 等）依赖 ResizeObserver，jsdom 未实现，需垫片（同备份存储页先例）。
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

/**
 * 文件存储配置页（FR-347，见 ADR-073）。
 * 覆盖 spec §3.7 验收：列表（内置/活跃徽章）/ 创建校验 / 草稿连通测试 / 设活跃（确认语义 +
 * 恰一条活跃）/ 删除守卫（422 用后端 message 呈现）/ SK 脱敏（编辑不回显明文）。
 */
describe('ArtifactStoragesPage（mock）', () => {
  beforeEach(() => {
    loginMockUser()
    // 删除走 DangerConfirm scope=platform，需平台管理员角色（测试 token 非 JWT 解不出 role）。
    useAuthStore.setState({ role: 10 })
  })

  it('渲染 seed 渠道：内置本机存储带内置+活跃徽章，s3 渠道显示端点与最近测试', async () => {
    renderWithProviders(<ArtifactStoragesPage />)
    const builtinRow = (await screen.findByText('本机存储')).closest('tr') as HTMLElement
    expect(within(builtinRow).getByText('内置')).toBeInTheDocument()
    expect(within(builtinRow).getByText('活跃')).toBeInTheDocument()
    // 内置行隐藏编辑/删除，仅测试可用（不可当普通渠道操作）。
    expect(within(builtinRow).queryByRole('button', { name: '编辑' })).not.toBeInTheDocument()
    expect(within(builtinRow).queryByRole('button', { name: '删除' })).not.toBeInTheDocument()

    const s3Row = screen.getByText('rustfs-主渠道').closest('tr') as HTMLElement
    expect(within(s3Row).getByText(/rustfs\.lan:9000/)).toBeInTheDocument()
    expect(within(s3Row).getByText('连接正常')).toBeInTheDocument()
    expect(within(s3Row).getByText('600s')).toBeInTheDocument()
  })

  it('创建渠道：endpoint/bucket 必填未齐时创建禁用，填齐提交后列表联动出现新行', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ArtifactStoragesPage />)
    await screen.findByText('本机存储')

    await user.click(screen.getByRole('button', { name: '新增 S3 渠道' }))
    const dialog = await screen.findByRole('dialog')
    // 表单文本框序：名称 / Bucket / Endpoint / Region / 前缀 / Access Key（SK 为 password 不在 textbox 角色）。
    const [nameInput, bucketInput, endpointInput] = within(dialog).getAllByRole('textbox')

    await user.type(nameInput, 'minio-新渠道')
    // endpoint/bucket 仍为空 → 校验未过，创建按钮禁用（必填校验）。
    expect(within(dialog).getByRole('button', { name: '创建' })).toBeDisabled()

    await user.type(bucketInput, 'jm-artifacts')
    await user.type(endpointInput, 'minio.local:9000')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    expect(await screen.findByText('minio-新渠道')).toBeInTheDocument()
    // seed 行仍在，确认是追加而非替换。
    expect(screen.getByText('rustfs-主渠道')).toBeInTheDocument()
  })

  it('未保存表单可先测试连接（真连语义走 mock）且不创建渠道', async () => {
    const user = userEvent.setup()
    let draftBody: Record<string, unknown> | null = null
    server.use(
      http.post(API('/artifact-storages/test'), async ({ request }) => {
        draftBody = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ ok: true, message: '草稿连接正常', latencyMs: 9 })
      }),
    )
    renderWithProviders(<ArtifactStoragesPage />)
    await screen.findByText('本机存储')

    await user.click(screen.getByRole('button', { name: '新增 S3 渠道' }))
    const dialog = await screen.findByRole('dialog')
    const [nameInput, bucketInput, endpointInput] = within(dialog).getAllByRole('textbox')
    await user.type(nameInput, 'draft-rustfs')
    await user.type(bucketInput, 'jm-artifacts')
    await user.type(endpointInput, 'rustfs.new:9000')

    await user.click(within(dialog).getByRole('button', { name: '测试连接' }))

    await waitFor(() => expect(draftBody?.endpoint).toBe('rustfs.new:9000'))
    expect(await screen.findByText('草稿连接正常')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.queryByRole('cell', { name: 'draft-rustfs' })).not.toBeInTheDocument()
  })

  it('设活跃走确认对话框，成功后活跃徽章移到新渠道（恰一条活跃）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ArtifactStoragesPage />)
    const s3Row = (await screen.findByText('rustfs-主渠道')).closest('tr') as HTMLElement

    await user.click(within(s3Row).getByRole('button', { name: '设活跃' }))
    // 确认语义：影响后续上传落点，先弹确认框。
    const confirm = await screen.findByRole('dialog')
    expect(within(confirm).getByText('切换活跃存储渠道？')).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: '设活跃' }))

    await waitFor(() => {
      const newS3Row = screen.getByText('rustfs-主渠道').closest('tr') as HTMLElement
      expect(within(newS3Row).getByText('活跃')).toBeInTheDocument()
    })
    const builtinRow = screen.getByText('本机存储').closest('tr') as HTMLElement
    expect(within(builtinRow).queryByText('活跃')).not.toBeInTheDocument()
    // 内置行失活后可再设活跃（local 恒可用语义）。
    expect(within(builtinRow).getByRole('button', { name: '设活跃' })).toBeInTheDocument()
  })

  it('删除守卫命中 → 422 用后端 message 呈现（被制品引用）', async () => {
    const user = userEvent.setup()
    const toastError = vi.spyOn(toast, 'error')
    renderWithProviders(<ArtifactStoragesPage />)
    const s3Row = (await screen.findByText('rustfs-主渠道')).closest('tr') as HTMLElement

    await user.click(within(s3Row).getByRole('button', { name: '删除' }))
    const confirm = await screen.findByRole('dialog')
    await user.click(within(confirm).getByRole('button', { name: '删除' }))

    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith(expect.stringContaining('被制品引用')),
    )
    // 守卫拒绝 → 行仍在。
    expect(screen.getByText('rustfs-主渠道')).toBeInTheDocument()
  })

  it('编辑不回显凭证明文：AK/SK 输入为空并以占位提示留空保留', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ArtifactStoragesPage />)
    const s3Row = (await screen.findByText('rustfs-主渠道')).closest('tr') as HTMLElement

    await user.click(within(s3Row).getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('编辑存储渠道')).toBeInTheDocument()

    const [nameInput, bucketInput, endpointInput, , , accessKeyInput] = within(dialog).getAllByRole('textbox')
    expect(nameInput).toHaveValue('rustfs-主渠道')
    expect(bucketInput).toHaveValue('jm-artifacts')
    expect(endpointInput).toHaveValue('rustfs.lan:9000')
    // SK 脱敏：不回显明文/密文，AK（text）与 SK（password）均空值 + 占位「已配置，留空保留」。
    expect(accessKeyInput).toHaveValue('')
    expect(accessKeyInput).toHaveAttribute('placeholder', '已配置，留空保留')
    const passwordInputs = Array.from((dialog as HTMLElement).querySelectorAll('input[type="password"]'))
    expect(passwordInputs).toHaveLength(1)
    expect(passwordInputs[0]).toHaveValue('')
    expect(passwordInputs[0]).toHaveAttribute('placeholder', '已配置，留空保留')
  })

  it('注入 500 → 显示空态而非崩溃', async () => {
    mockInject('get', '/artifact-storages', { kind: 'status', status: 500 })
    renderWithProviders(<ArtifactStoragesPage />)
    expect(await screen.findByText('暂无存储渠道')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('rustfs-主渠道')).not.toBeInTheDocument()
    })
  })
})
