import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import type { ImportInspectResult } from '@/api/importServer'

/**
 * FR-302 导入现有服务器向导流程 DOM 测。
 * 桩掉 DirectoryPicker（避开真实目录浏览）与 importServer/nodes hooks，
 * 驱动：预置节点 → 选目录触发探测 → 探测结果展示 jar → 进入模式步选「搬进托管区」→ 配置步提交。
 * 强断言：探测请求参数、模式二选一双呈现、提交时携带所选 mode/jarPath。
 */

const inspectResult: ImportInspectResult = {
  jars: [{ path: 'server.jar', size: 45_000_000, mainClassHint: 'io.papermc.Main' }],
  jdks: [{ path: '/srv/paper/jre', vendor: 'Temurin', version: '21.0.11', majorVersion: 21, arch: 'x64' }],
  serverPort: 25565,
  eulaAccepted: true,
  propsFound: true,
}

const inspectMutate = vi.fn((_vars: unknown, opts?: { onSuccess?: (r: ImportInspectResult) => void }) => {
  opts?.onSuccess?.(inspectResult)
})
const importMutate = vi.fn((_vars: unknown, opts?: { onSuccess?: (i: { id: number; name: string }) => void }) => {
  opts?.onSuccess?.({ id: 7, name: 'old-server' })
})

vi.mock('@/api/importServer', () => ({
  useInspectImportDir: () => ({ mutate: inspectMutate, isPending: false, reset: vi.fn() }),
  useImportServer: () => ({ mutate: importMutate, isPending: false }),
}))
vi.mock('@/api/nodes', () => ({
  useNodes: () => ({ data: [{ id: 1, name: 'node-a', status: 1 }] }),
}))
vi.mock('@/api/jdks', () => ({
  useNodeJDKs: () => ({ data: [] }),
}))
const checkNodePathAccess = vi.fn(async () => ({
  exists: true,
  isDir: true,
  readable: true,
  writable: true,
}))
const chmodNodePath = vi.fn(async () => ({ modeOctal: '0755' }))
vi.mock('@/api/nodeRuntime', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/nodeRuntime')>()
  return {
    ...actual,
    checkNodePathAccess: (...args: unknown[]) => checkNodePathAccess(...args),
    chmodNodePath: (...args: unknown[]) => chmodNodePath(...args),
  }
})
vi.mock('@/components/DirectoryPicker', () => ({
  __esModule: true,
  default: ({ onPick }: { onPick: (p: string) => void }) => (
    <button type="button" onClick={() => onPick('/srv/paper')}>pick-dir</button>
  ),
}))

import ImportServerWizard from './ImportServerWizard'

beforeEach(() => {
  inspectMutate.mockClear()
  importMutate.mockClear()
  checkNodePathAccess.mockClear()
  chmodNodePath.mockClear()
  checkNodePathAccess.mockResolvedValue({
    exists: true,
    isDir: true,
    readable: true,
    writable: true,
  })
})

describe('ImportServerWizard（FR-302 / FR-374）', () => {
  it('手输绝对路径触发探测（FR-374）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ImportServerWizard open onClose={vi.fn()} initialNodeId={1} />)
    const input = await screen.findByLabelText(/绝对路径|Absolute path/i)
    await user.clear(input)
    await user.type(input, '/home/wxys233/server')
    await user.click(screen.getByRole('button', { name: /探测|Inspect/i }))
    expect(inspectMutate).toHaveBeenCalledWith(
      expect.objectContaining({ nodeId: 1, path: '/home/wxys233/server' }),
      expect.anything(),
    )
  })

  it('权限失败展示诊断区（FR-374）', async () => {
    inspectMutate.mockImplementationOnce((_vars: unknown, opts?: { onError?: (e: Error) => void }) => {
      opts?.onError?.(
        Object.assign(new Error('permission denied'), {
          response: { data: { message: '没有权限读取该目录（Worker 用户无法列出内容）' } },
        }),
      )
    })
    const user = userEvent.setup()
    renderWithProviders(<ImportServerWizard open onClose={vi.fn()} initialNodeId={1} />)
    await user.click(await screen.findByRole('button', { name: 'pick-dir' }))
    expect(await screen.findByTestId('import-perm-error')).toBeInTheDocument()
    expect(screen.getByText(/没有权限读取该目录/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /尝试修复权限|Try fix/i })).toBeInTheDocument()
  })

  it('探测→模式二选一→提交，携带所选 mode 与 jarPath', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ImportServerWizard open onClose={vi.fn()} initialNodeId={1} />)

    // dir 步：预置节点，桩 DirectoryPicker 选目录 → 触发探测
    await user.click(await screen.findByRole('button', { name: 'pick-dir' }))
    expect(inspectMutate).toHaveBeenCalledWith(
      expect.objectContaining({ nodeId: 1, path: '/srv/paper' }),
      expect.anything(),
    )

    // inspect 步：探测结果的 jar 候选呈现（radio 已默认选中第一个）
    const jarRadio = await screen.findByRole('radio', { name: /server\.jar/i }).catch(() => null)
    // jar 以 label 包裹 radio，文本在 label 内——按结构断言候选文本存在
    expect(await screen.findByText('server.jar')).toBeInTheDocument()

    // 下一步 → mode 步：就地接管 / 搬进托管区 二选一双呈现
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(await screen.findByText('就地接管')).toBeInTheDocument()
    expect(screen.getByText('搬进托管区')).toBeInTheDocument()

    // 选「搬进托管区」
    const modeRadios = document.querySelectorAll('input[name="import-mode"]')
    expect(modeRadios).toHaveLength(2)
    await user.click(modeRadios[1] as HTMLElement)

    // 下一步 → config 步：名称预填目录名，提交
    await user.click(screen.getByRole('button', { name: '下一步' }))
    const submitBtn = await screen.findByRole('button', { name: '导入' })
    await user.click(submitBtn)

    await waitFor(() =>
      expect(importMutate).toHaveBeenCalledWith(
        expect.objectContaining({ nodeId: 1, path: '/srv/paper', mode: 'migrate', jarPath: 'server.jar' }),
        expect.anything(),
      ),
    )
    void jarRadio
  })

  it('open=false 不渲染', () => {
    renderWithProviders(<ImportServerWizard open={false} onClose={vi.fn()} initialNodeId={1} />)
    expect(screen.queryByRole('button', { name: 'pick-dir' })).not.toBeInTheDocument()
  })
})
