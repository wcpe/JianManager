import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import type { PMConfigView } from '@/api/pmConfig'

/**
 * FR-306 节点包管理器子区 DOM 测：渲染当前 PM + registry（token 脱敏），
 * 切 PM + 加源 + 保存 → 提交携带 pm 与 registry。
 */

const view: PMConfigView = {
  pm: 'npm',
  corepackAvailable: true,
  pmVersion: '10.0.0',
  nodeBin: '/opt/runtimes/nodejs-22/bin/node',
  registries: [{ url: 'https://registry.npmmirror.com', scope: '' }],
}

const saveMutate = vi.fn(
  (_v: unknown, opts?: { onSuccess?: () => void }) => opts?.onSuccess?.(),
)

vi.mock('@/api/pmConfig', async (orig) => {
  const actual = await orig<typeof import('@/api/pmConfig')>()
  return {
    ...actual,
    useNodePMConfig: () => ({ data: view, isLoading: false }),
    useSetNodePMConfig: () => ({ mutate: saveMutate, isPending: false }),
  }
})

import NodePMConfigSection from './NodePMConfigSection'

beforeEach(() => saveMutate.mockClear())

describe('NodePMConfigSection（FR-306）', () => {
  it('选 pnpm + 加源 + 保存，提交携带 pm 与 registries', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodePMConfigSection nodeId={2} />)

    // 当前 PM 与版本可见
    expect(await screen.findByText(/npm \(10\.0\.0\)/)).toBeInTheDocument()

    // 选 pnpm
    await user.click(screen.getByRole('button', { name: /pnpm/ }))

    // 保存
    await user.click(screen.getByRole('button', { name: '保存配置' }))
    await waitFor(() =>
      expect(saveMutate).toHaveBeenCalledWith(
        expect.objectContaining({
          pm: 'pnpm',
          registries: expect.arrayContaining([
            expect.objectContaining({ url: 'https://registry.npmmirror.com' }),
          ]),
        }),
        expect.anything(),
      ),
    )
  })

  it('渲染包管理器区标题', () => {
    renderWithProviders(<NodePMConfigSection nodeId={2} />)
    expect(screen.getByText('包管理器与下载源')).toBeInTheDocument()
  })

  it('registries=null（老 CP nil 切片序列化）不崩，渲染默认空 registry 行', async () => {
    // v0.15.0 真机测试抓出：全新节点后端回 "registries":null，
    // data.registries.length 直接 TypeError → /nodes?tab=jdk 整页白屏。
    const orig = view.registries
    ;(view as { registries: unknown }).registries = null
    try {
      renderWithProviders(<NodePMConfigSection nodeId={1} />)
      expect(await screen.findByText('包管理器与下载源')).toBeInTheDocument()
      // 默认给一条空 registry 行可编辑
      expect(screen.getByRole('textbox', { name: 'registry 地址' })).toBeInTheDocument()
    } finally {
      view.registries = orig
    }
  })
})
