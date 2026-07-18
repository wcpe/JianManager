import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import ArtifactStoragesPage from './ArtifactStoragesPage'

// 对话框内 Radix 组件依赖 ResizeObserver，jsdom 未实现，需垫片（同本页既有测试）。
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

/** 构造迁移状态 payload（GET /artifact-storages/migration 形态）。 */
function migrationPayload(overrides: {
  state: 'running' | 'succeeded' | 'failed' | 'canceled'
  progress?: number
  migrated?: number
  failed?: number
}) {
  return {
    task: {
      id: 1,
      taskId: 'mig-t1',
      nodeId: 0,
      kind: 'artifact_migrate',
      state: overrides.state,
      progress: overrides.progress ?? 40,
      title: '制品存量迁移 → rustfs-主渠道',
      detail: '',
      error: '',
      result: '',
      cancelRequested: false,
      createdBy: 1,
      createdAt: '2026-07-17T08:00:00Z',
      updatedAt: '2026-07-17T08:00:10Z',
    },
    migration: {
      taskId: 'mig-t1',
      targetChannelId: 2,
      targetName: 'rustfs-主渠道',
      total: 5,
      migrated: overrides.migrated ?? 2,
      failed: overrides.failed ?? 0,
      skipped: 1,
    },
  }
}

/**
 * 存量迁移 UI（FR-348）：迁移入口/确认模态发起、在途进度卡与入口禁用、
 * 失败明细模态与重新发起、409 用后端 message 呈现。
 */
describe('ArtifactStoragesPage 存量迁移（mock）', () => {
  beforeEach(() => {
    loginMockUser()
    useAuthStore.setState({ role: 10 })
  })

  it('迁移入口走确认模态，确认后发起任务并出现在途进度卡与计数', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ArtifactStoragesPage />)
    const s3Row = (await screen.findByText('rustfs-主渠道')).closest('tr') as HTMLElement

    await user.click(within(s3Row).getByRole('button', { name: '迁移到此' }))
    const confirm = await screen.findByRole('dialog')
    expect(within(confirm).getByText('迁移存量制品到「rustfs-主渠道」？')).toBeInTheDocument()
    // 语义说明：先改记录再删源 + 跳过 + 可续跑。
    expect(within(confirm).getByText(/更新记录后再删除源副本/)).toBeInTheDocument()

    await user.click(within(confirm).getByRole('button', { name: '开始迁移' }))

    // devmock 模拟：POST 建 running 任务，GET 轮询逐拍推进（共 3 · 跳过 1 · 两拍迁完）。
    expect(await screen.findByText('存量迁移进行中 → rustfs-主渠道')).toBeInTheDocument()
    expect(await screen.findByText('共 3 · 已迁 1 · 失败 0 · 跳过 1')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '强制停止' })).toBeInTheDocument()
  })

  it('在途迁移时各行迁移入口禁用', async () => {
    server.use(
      http.get(API('/artifact-storages/migration'), () =>
        HttpResponse.json(migrationPayload({ state: 'running', progress: 40, migrated: 2 })),
      ),
    )
    renderWithProviders(<ArtifactStoragesPage />)
    await screen.findByText('存量迁移进行中 → rustfs-主渠道')
    expect(screen.getByText('共 5 · 已迁 2 · 失败 0 · 跳过 1')).toBeInTheDocument()

    for (const btn of screen.getAllByRole('button', { name: '迁移到此' })) {
      expect(btn).toBeDisabled()
    }
  })

  it('devmock 发起的在途任务可由通用任务接口强制停止并展示终态摘要', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ArtifactStoragesPage />)
    const s3Row = (await screen.findByText('rustfs-主渠道')).closest('tr') as HTMLElement

    await user.click(within(s3Row).getByRole('button', { name: '迁移到此' }))
    const confirm = await screen.findByRole('dialog')
    await user.click(within(confirm).getByRole('button', { name: '开始迁移' }))
    await screen.findByText('存量迁移进行中 → rustfs-主渠道')
    await user.click(screen.getByRole('button', { name: '强制停止' }))

    expect(await screen.findByText('上次迁移 → rustfs-主渠道')).toBeInTheDocument()
    expect(screen.getByText('已停止')).toBeInTheDocument()
  })

  it('终态失败显示上次迁移摘要，失败明细模态列 sha256+原因，重新发起=对同目标重试', async () => {
    const user = userEvent.setup()
    let retryUrl: string | null = null
    server.use(
      http.get(API('/artifact-storages/migration'), () =>
        HttpResponse.json(migrationPayload({ state: 'failed', progress: 100, migrated: 2, failed: 2 })),
      ),
      http.get(API('/artifact-storages/migration/:taskId/failures'), () =>
        HttpResponse.json([
          {
            id: 1, taskId: 'mig-t1', assetId: 11,
            sha256: 'a'.repeat(64), filename: 'modpack.jar', size: 1024,
            reason: '写入目标失败: HTTP 500', createdAt: '2026-07-17T08:00:05Z',
          },
          {
            id: 2, taskId: 'mig-t1', assetId: 12,
            sha256: 'b'.repeat(64), filename: 'assets.zip', size: 2048,
            reason: '源内容 sha256 校验不符', createdAt: '2026-07-17T08:00:06Z',
          },
        ]),
      ),
      http.post(API('/artifact-storages/:id/migrate'), ({ request }) => {
        retryUrl = new URL(request.url).pathname
        return HttpResponse.json({ taskId: 'mig-t2' }, { status: 202 })
      }),
    )
    renderWithProviders(<ArtifactStoragesPage />)

    expect(await screen.findByText('上次迁移 → rustfs-主渠道')).toBeInTheDocument()
    expect(screen.getByText('已失败')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '失败明细' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('迁移失败明细')).toBeInTheDocument()
    expect(await within(dialog).findByText('modpack.jar')).toBeInTheDocument()
    expect(within(dialog).getByText('写入目标失败: HTTP 500')).toBeInTheDocument()
    expect(within(dialog).getByText('源内容 sha256 校验不符')).toBeInTheDocument()
    expect(within(dialog).getByText('a'.repeat(12))).toBeInTheDocument()
    expect(within(dialog).getByText('1.0 KB')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '重新发起' }))
    await waitFor(() => expect(retryUrl).toContain('/artifact-storages/2/migrate'))
    // 发起后失败明细模态关闭。
    await waitFor(() => expect(screen.queryByText('迁移失败明细')).not.toBeInTheDocument())
  })

  it('并发发起 409 → 用后端 message 呈现', async () => {
    const user = userEvent.setup()
    const toastError = vi.spyOn(toast, 'error')
    server.use(
      http.post(API('/artifact-storages/:id/migrate'), () =>
        HttpResponse.json(
          { error: 'MIGRATION_IN_FLIGHT', message: '已有制品迁移任务在途，同一时间仅允许一个迁移任务' },
          { status: 409 },
        ),
      ),
    )
    renderWithProviders(<ArtifactStoragesPage />)
    const s3Row = (await screen.findByText('rustfs-主渠道')).closest('tr') as HTMLElement

    await user.click(within(s3Row).getByRole('button', { name: '迁移到此' }))
    const confirm = await screen.findByRole('dialog')
    await user.click(within(confirm).getByRole('button', { name: '开始迁移' }))

    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith(expect.stringContaining('已有制品迁移任务在途')),
    )
  })

  it('目标探测失败 422 → 用后端 message 呈现', async () => {
    const user = userEvent.setup()
    const toastError = vi.spyOn(toast, 'error')
    server.use(
      http.post(API('/artifact-storages/:id/migrate'), () =>
        HttpResponse.json(
          { error: 'BUSINESS_ERROR', message: '目标渠道连接失败：rustfs endpoint 不可达' },
          { status: 422 },
        ),
      ),
    )
    renderWithProviders(<ArtifactStoragesPage />)
    const s3Row = (await screen.findByText('rustfs-主渠道')).closest('tr') as HTMLElement

    await user.click(within(s3Row).getByRole('button', { name: '迁移到此' }))
    const confirm = await screen.findByRole('dialog')
    await user.click(within(confirm).getByRole('button', { name: '开始迁移' }))

    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith(expect.stringContaining('目标渠道连接失败')),
    )
  })
})
