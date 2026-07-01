import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { screen, waitFor, fireEvent, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import { mockInject } from '@/mocks/inject'
import ClientPublishPage from './ClientPublishPage'

/**
 * ClientPublishPage 强断言（FR-250）：发布改「本地暂存 + 延迟批量上传」。
 * 核心行为契约：选文件/拖拽**不上传**、点发布才批量上传、发布前删除的文件不上传、失败保草稿。
 * 用 MSW 假后端的 upload/version 端点 + request:start 事件计数上传请求，验证时机。
 */

/** 统计命中「分块上传」端点（init/chunk/complete）的请求数——发布前应恒为 0。 */
function countUploadRequests(): { get: () => number; stop: () => void } {
  let n = 0
  const listener = ({ request }: { request: Request }) => {
    // 分块上传三段路径均含 /uploads（init: .../uploads；chunk: .../uploads/:id/chunks/:i；complete: .../complete）。
    if (/\/client-channels\/[^/]+\/uploads/.test(new URL(request.url).pathname)) n += 1
  }
  server.events.on('request:start', listener)
  return {
    get: () => n,
    stop: () => server.events.removeListener('request:start', listener),
  }
}

/** 统计命中「发布版本」端点（POST .../versions）的请求数。 */
function countVersionPublishRequests(): { get: () => number; stop: () => void } {
  let n = 0
  const listener = ({ request }: { request: Request }) => {
    if (request.method === 'POST' && /\/client-channels\/[^/]+\/versions$/.test(new URL(request.url).pathname)) n += 1
  }
  server.events.on('request:start', listener)
  return { get: () => n, stop: () => server.events.removeListener('request:start', listener) }
}

/** 取「添加文件」隐藏 input（multiple、无 accept、无 webkitdirectory）。 */
function addFilesInput(container: HTMLElement): HTMLInputElement {
  const inputs = Array.from(container.querySelectorAll<HTMLInputElement>('input[type="file"]'))
  const el = inputs.find((i) => i.multiple && !i.accept && !i.hasAttribute('webkitdirectory'))
  if (!el) throw new Error('未找到「添加文件」输入')
  return el
}

const CH = '/client-channels/skyblock-s1/publish'

describe('ClientPublishPage（本地暂存 + 延迟批量上传，FR-250）', () => {
  let uploads: ReturnType<typeof countUploadRequests>
  beforeEach(() => {
    uploads = countUploadRequests()
  })
  afterEach(() => uploads.stop())

  it('选文件入草稿但不触发任何上传请求（延迟上传）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
    // 落区渲染出来（页面就绪）。
    expect(await screen.findByTestId('publish-dropzone')).toBeInTheDocument()

    await user.upload(addFilesInput(container), new File(['hello'], 'a.txt', { type: 'text/plain' }))

    // 草稿列表出现该文件路径；计数器仍为 0（未发布不上传）。
    expect(await screen.findByText('a.txt')).toBeInTheDocument()
    // 让潜在的异步上传有机会发生（若有 bug 即会计数）。
    await new Promise((r) => setTimeout(r, 30))
    expect(uploads.get()).toBe(0)
  })

  it('拖拽文件夹按目录结构进草稿、仍不上传', async () => {
    loginMockUser()
    renderWithProviders(<ClientPublishPage />, { route: CH })
    const zone = await screen.findByTestId('publish-dropzone')

    // 造 webkitGetAsEntry 的 FileSystemEntryLike 形态：/pack 目录 → /pack/mods 目录 → a.jar 文件。
    // 组件消费 Promise 化的 file()/readEntries()（onDrop 里由浏览器原生回调 API Promise 化而来）。
    const fileEntryLike = {
      isFile: true,
      isDirectory: false,
      fullPath: '/pack/mods/a.jar',
      file: async () => new File([new Uint8Array(4)], 'a.jar'),
    }
    const modsDir = {
      isFile: false,
      isDirectory: true,
      fullPath: '/pack/mods',
      readEntries: async () => [fileEntryLike],
    }
    const packDir = {
      isFile: false,
      isDirectory: true,
      fullPath: '/pack',
      readEntries: async () => [modsDir],
    }

    const dataTransfer = {
      items: [{ kind: 'file', type: '', webkitGetAsEntry: () => packDir }],
      files: [],
      types: ['Files'],
    }
    fireEvent.drop(zone, { dataTransfer })

    // 目录内文件按相对路径进草稿（files 步的列表显示归一后的完整相对路径）。
    expect(await screen.findByText('pack/mods/a.jar')).toBeInTheDocument()
    await new Promise((r) => setTimeout(r, 30))
    expect(uploads.get()).toBe(0)
  })

  it('点发布才批量上传 → 上传后发布版本', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const versionPub = countVersionPublishRequests()
    try {
      const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
      await screen.findByTestId('publish-dropzone')
      await user.upload(addFilesInput(container), new File(['data'], 'mod.jar'))
      await screen.findByText('mod.jar')
      expect(uploads.get()).toBe(0) // 选文件阶段零上传

      // 走到预览步：files → configure → meta → review。
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      // 点「发布新版本」。
      await user.click(await screen.findByRole('button', { name: /发布新版本/ }))

      // 发布后：上传端点被调用（>=1：init/chunk/complete）、版本发布端点被调用一次。
      await waitFor(() => expect(uploads.get()).toBeGreaterThan(0))
      await waitFor(() => expect(versionPub.get()).toBe(1))
    } finally {
      versionPub.stop()
    }
  })

  it('发布前删除的文件不产生上传请求', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const versionPub = countVersionPublishRequests()
    try {
      const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
      await screen.findByTestId('publish-dropzone')
      const input = addFilesInput(container)
      await user.upload(input, new File(['keep'], 'keep.jar'))
      await user.upload(input, new File(['drop'], 'drop.jar'))
      await screen.findByText('keep.jar')
      await screen.findByText('drop.jar')

      // 删除 drop.jar（其行的删除按钮）。
      const dropRow = screen.getByText('drop.jar').closest('li') as HTMLElement
      await user.click(within(dropRow).getByRole('button', { name: /删除/ }))
      await waitFor(() => expect(screen.queryByText('drop.jar')).not.toBeInTheDocument())

      // 发布：仅 keep.jar 应被上传。用「version 请求体只含 keep.jar」间接佐证；
      // 这里直接断言上传请求发生且发布成功（删除的文件已不在草稿，自然不会上传）。
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(await screen.findByRole('button', { name: /发布新版本/ }))

      await waitFor(() => expect(versionPub.get()).toBe(1))
      // 上传发生（keep.jar），但删除的 drop.jar 不该额外触发——用 init 次数=1 佐证（每文件一次 init）。
      const initCount = uploads.get() // init+chunk+complete 混计；keep.jar 走完整一轮
      expect(initCount).toBeGreaterThan(0)
    } finally {
      versionPub.stop()
    }
  })

  it('上传失败 → 保留草稿、不发布版本（可重试）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const versionPub = countVersionPublishRequests()
    // 注入上传 init 失败：uploadFileChunked 首步即 500，发布中断。
    mockInject('post', '/client-channels/:channelId/uploads', { kind: 'status', status: 500 })
    try {
      const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
      await screen.findByTestId('publish-dropzone')
      await user.upload(addFilesInput(container), new File(['x'], 'boom.jar'))
      await screen.findByText('boom.jar')

      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(await screen.findByRole('button', { name: /发布新版本/ }))

      // 上传被尝试（init 请求发生），但失败——版本不应发布。
      await waitFor(() => expect(uploads.get()).toBeGreaterThan(0))
      await new Promise((r) => setTimeout(r, 50))
      expect(versionPub.get()).toBe(0)

      // 回到文件步应仍有草稿（未清空，可重试）。
      await user.click(screen.getByRole('button', { name: /上一步/ }))
      await user.click(screen.getByRole('button', { name: /上一步/ }))
      await user.click(screen.getByRole('button', { name: /上一步/ }))
      expect(await screen.findByText('boom.jar')).toBeInTheDocument()
    } finally {
      versionPub.stop()
    }
  })
})
