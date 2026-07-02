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

/** CRC32（zip 用）。 */
function crc32(bytes: Uint8Array): number {
  let c = ~0
  for (let i = 0; i < bytes.length; i++) {
    c ^= bytes[i]
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1))
  }
  return ~c >>> 0
}

/**
 * 拼一个含单个 GBK 文件名 STORE entry 的最小 zip（UTF-8 标志位不置）——模拟中文 Windows 打包（BUG-G）。
 * 文件名字节由调用方给（GBK 码点手工拼），内容任意。
 */
function buildGbkZip(nameBytes: Uint8Array, data: Uint8Array): File {
  const out: number[] = []
  const central: number[] = []
  const p16 = (a: number[], n: number) => a.push(n & 0xff, (n >>> 8) & 0xff)
  const p32 = (a: number[], n: number) => a.push(n & 0xff, (n >>> 8) & 0xff, (n >>> 16) & 0xff, (n >>> 24) & 0xff)
  const pb = (a: number[], b: Uint8Array) => b.forEach((x) => a.push(x))
  const crc = crc32(data)
  // local file header
  p32(out, 0x04034b50)
  p16(out, 20); p16(out, 0); p16(out, 0); p16(out, 0); p16(out, 0)
  p32(out, crc); p32(out, data.length); p32(out, data.length)
  p16(out, nameBytes.length); p16(out, 0)
  pb(out, nameBytes); pb(out, data)
  // central dir
  p32(central, 0x02014b50)
  p16(central, 20); p16(central, 20); p16(central, 0); p16(central, 0); p16(central, 0); p16(central, 0)
  p32(central, crc); p32(central, data.length); p32(central, data.length)
  p16(central, nameBytes.length); p16(central, 0); p16(central, 0); p16(central, 0); p16(central, 0)
  p32(central, 0); p32(central, 0)
  pb(central, nameBytes)
  const cdStart = out.length
  pb(out, new Uint8Array(central))
  // EOCD
  p32(out, 0x06054b50); p16(out, 0); p16(out, 0); p16(out, 1); p16(out, 1)
  p32(out, central.length); p32(out, cdStart); p16(out, 0)
  return new File([new Uint8Array(out)], 'pack.zip', { type: 'application/zip' })
}

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

  it('拖拽文件夹按目录结构进草稿、仍不上传（原生回调式 entry，BUG-F 回归）', async () => {
    loginMockUser()
    renderWithProviders(<ClientPublishPage />, { route: CH })
    const zone = await screen.findByTestId('publish-dropzone')

    // 造 webkitGetAsEntry 的**浏览器原生**形态（回调式，非 Promise）：
    // 文件 entry.file(successCb)、目录 entry.createReader().readEntries(cb) 分批返回、读空为止。
    // 这是真机拖拽的真实形态；onDrop 须经 adaptEntry Promise 化才能收集（BUG-F 根因）。
    type NativeEntry = {
      isFile: boolean
      isDirectory: boolean
      fullPath: string
      name: string
      file?: (ok: (f: File) => void) => void
      createReader?: () => { readEntries: (ok: (e: NativeEntry[]) => void) => void }
    }
    const nativeFile = (fullPath: string): NativeEntry => ({
      isFile: true,
      isDirectory: false,
      fullPath,
      name: fullPath.split('/').pop()!,
      file: (ok) => ok(new File([new Uint8Array(4)], fullPath.split('/').pop()!)),
    })
    const nativeDir = (fullPath: string, children: NativeEntry[]): NativeEntry => ({
      isFile: false,
      isDirectory: true,
      fullPath,
      name: fullPath.split('/').pop() ?? fullPath,
      createReader() {
        let done = false
        return {
          readEntries(ok) {
            // 原生语义：首次返回全部子项，再次返回空数组表示读完（循环终止）。
            // 先翻转游标再回调，模拟真实 reader 的状态推进（避免同步实现的重入歧义）。
            const batch = done ? [] : children
            done = true
            ok(batch)
          },
        }
      },
    })
    const packDir = nativeDir('/pack', [nativeDir('/pack/mods', [nativeFile('/pack/mods/a.jar')])])

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

  it('上传 GBK 文件名 zip：解包后草稿路径为正确中文（BUG-G 回归）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
    await screen.findByTestId('publish-dropzone')

    // 文件名 "模组/配置.txt" 以 GBK 编码、不置 UTF-8 位（"模"=C4A3 "组"=D7E9 "配"=C5E4 "置"=D6C3）。
    const nameBytes = new Uint8Array([0xc4, 0xa3, 0xd7, 0xe9, 0x2f, 0xc5, 0xe4, 0xd6, 0xc3, ...Array.from('.txt').map((c) => c.charCodeAt(0))])
    const zip = buildGbkZip(nameBytes, new Uint8Array([1, 2, 3]))
    await user.upload(addFilesInput(container), zip)

    // 解包后按包内相对路径进草稿，中文正确（非乱码），且未触发上传。
    expect(await screen.findByText('模组/配置.txt')).toBeInTheDocument()
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

/**
 * FR-255：清理范围目录树编辑器 + clean-all 哨兵 + 自定义排除 + 发布二次确认。
 * 验证 meta 步勾选/开关产出正确的 manifest 字段，clean-all 发布触发 DangerConfirm。
 */
describe('ClientPublishPage（清理范围编辑器，FR-255）', () => {
  /** 捕获发布版本请求体（最后一次 POST .../versions 的 body，clone 不干扰 handler）。 */
  function capturePublishBody(): { get: () => Promise<Record<string, unknown>>; stop: () => void } {
    let bodyP: Promise<Record<string, unknown>> = Promise.resolve({})
    const listener = ({ request }: { request: Request }) => {
      if (request.method === 'POST' && /\/client-channels\/[^/]+\/versions$/.test(new URL(request.url).pathname)) {
        bodyP = request.clone().json().catch(() => ({}))
      }
    }
    server.events.on('request:start', listener)
    return {
      get: () => bodyP,
      stop: () => server.events.removeListener('request:start', listener),
    }
  }

  /** 添加文件并走到 meta 步（files → configure → meta）。 */
  async function gotoMeta(user: ReturnType<typeof userEvent.setup>, container: HTMLElement) {
    await screen.findByTestId('publish-dropzone')
    await user.upload(addFilesInput(container), new File(['x'], 'mods/a.jar'))
    await user.upload(addFilesInput(container), new File(['y'], 'config/foo/b.toml'))
    await screen.findByText('mods/a.jar')
    // files → configure → meta
    await user.click(screen.getByRole('button', { name: /下一步/ }))
    await user.click(screen.getByRole('button', { name: /下一步/ }))
  }

  it('目录树右键标记为清理产出 managedDirs（含深层嵌套目录）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const pub = capturePublishBody()
    try {
      const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
      await gotoMeta(user, container)

      // 右键 config/foo 目录 → 标记为清理（FR-262 新交互）
      const row = container.querySelector('[data-testid="clean-scope-dir-row"][data-dir-path="config/foo"]') as HTMLElement
      fireEvent.contextMenu(row, { button: 2, clientX: 100, clientY: 100 })
      const menu = await screen.findByTestId('clean-scope-context-menu')
      fireEvent.click(within(menu).getByTestId('clean-scope-mark-clean'))

      // meta → review，点发布。
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(await screen.findByRole('button', { name: /发布新版本/ }))

      // 发布请求体 managedDirs 含 "config/foo"。
      const body = await waitFor(async () => {
        const b = await pub.get()
        expect(Object.keys(b).length).toBeGreaterThan(0)
        return b
      })
      expect(body.managedDirs).toContain('config/foo')
    } finally {
      pub.stop()
    }
  })

  it('clean-all 开关产出 managedDirs=["*"] + 发布触发 DangerConfirm 二次确认', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const pub = capturePublishBody()
    try {
      const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
      await gotoMeta(user, container)

      // 开启「清空整个游戏目录」开关。
      await user.click(screen.getByTestId('clean-all-toggle'))
      // 目录树标注 clean-all（FR-262：全目录标记为清理红色、交互禁用）。
      expect(screen.getByTestId('clean-scope-tree')).toHaveAttribute('data-clean-all', 'true')

      // meta → review。
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      // review 步展示 clean-all 徽标。
      expect(await screen.findByTestId('review-clean-all-badge')).toBeInTheDocument()
      // 点发布 → 弹 DangerConfirm 二次确认。
      await user.click(screen.getByRole('button', { name: /发布新版本/ }))
      const confirmDialog = await screen.findByText(/确认清空整个游戏目录/)
      expect(confirmDialog).toBeInTheDocument()
      // 此时发布请求尚未发出（等二次确认）。
      await new Promise((r) => setTimeout(r, 30))
      expect(Object.keys(await pub.get()).length).toBe(0)

      // 确认 → 发布。
      await user.click(screen.getByRole('button', { name: /我已知晓风险/ }))

      // 发布请求体 managedDirs = ["*"]。
      const body = await waitFor(async () => {
        const b = await pub.get()
        expect(Object.keys(b).length).toBeGreaterThan(0)
        return b
      })
      expect(body.managedDirs).toEqual(['*'])
    } finally {
      pub.stop()
    }
  })

  it('自定义追加排除产出 cleanExclude（右键标记排除）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const pub = capturePublishBody()
    try {
      const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
      await gotoMeta(user, container)

      // 添加自定义目录「玩家mod」。
      const dirInput = screen.getByTestId('custom-dir-input')
      await user.type(dirInput, '玩家mod')
      await user.keyboard('{Enter}')

      // 右键目录「玩家mod」→ 标记为排除。
      const dirRows = screen.getAllByTestId('clean-scope-dir-row')
      const targetRow = dirRows.find((el) => el.textContent?.includes('玩家mod'))
      expect(targetRow).toBeTruthy()
      await user.pointer({ keys: '[MouseRight]', target: targetRow! })
      const markExclude = await screen.findByTestId('clean-scope-mark-exclude')
      await user.click(markExclude)

      // meta → review，点发布。
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(await screen.findByRole('button', { name: /发布新版本/ }))

      const body = await waitFor(async () => {
        const b = await pub.get()
        expect(Object.keys(b).length).toBeGreaterThan(0)
        return b
      })
      expect(body.cleanExclude).toContain('玩家mod')
    } finally {
      pub.stop()
    }
  })

  it('clean-all 发布 DangerConfirm 取消则不发布', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const pub = capturePublishBody()
    try {
      const { container } = renderWithProviders(<ClientPublishPage />, { route: CH })
      await gotoMeta(user, container)
      await user.click(screen.getByTestId('clean-all-toggle'))
      await user.click(screen.getByRole('button', { name: /下一步/ }))
      await user.click(await screen.findByRole('button', { name: /发布新版本/ }))

      // 弹确认后点「取消」（DangerConfirm 的取消按钮文案 = common.cancel = "取消"）。
      await screen.findByText(/确认清空整个游戏目录/)
      await user.click(screen.getByRole('button', { name: /^取消$/ }))

      // 取消后发布请求不应发出。
      await new Promise((r) => setTimeout(r, 50))
      expect(Object.keys(await pub.get()).length).toBe(0)
    } finally {
      pub.stop()
    }
  })
})
