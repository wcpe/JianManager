import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@/i18n'

// 管理面内容/下载端点 mock（FR-214）：验证共享 FileBrowser 经 clientDistSource 真正接入预览。
const { fetchMock, downloadMock } = vi.hoisted(() => ({ fetchMock: vi.fn(), downloadMock: vi.fn() }))
vi.mock('@/api/clientVersions', () => ({
  fetchClientArtifactContent: fetchMock,
  downloadClientArtifact: downloadMock,
}))

import FileBrowser from '../FileBrowser'
import { clientDistSource } from './clientDistSource'

// jsdom 缺 Range 几何，CodeMirror 6 坐标测量会抛——补最小零矩形垫片（与 FileBrowser.dom.test 同范式）。
beforeAll(() => {
  const zeroRects = () =>
    ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList
  Range.prototype.getClientRects = zeroRects
  Range.prototype.getBoundingClientRect = () =>
    ({ x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, toJSON: () => ({}) }) as DOMRect
})

const FILES = [
  { path: 'config/server.properties', size: 50, artifactSha: 'sha-cfg' },
  { path: 'mods/foo.jar', size: 1024, artifactSha: 'sha-foo' },
]

describe('客户端分发预览经共享 FileBrowser 接入（FR-214）', () => {
  beforeEach(() => {
    fetchMock.mockReset()
    downloadMock.mockReset()
  })

  it('扁平清单成树（含目录与文件），点文本文件 → 高亮预览出内容', async () => {
    fetchMock.mockResolvedValue({ kind: 'text', content: 'motd=Hello\nmax-players=20', size: 50, codec: 'none' })
    const user = userEvent.setup()
    const { container } = render(<FileBrowser source={clientDistSource('ch1', FILES)} />)

    // 目录（config/mods）与文件（server.properties）都渲染（buildTree 由扁平 path 推导目录）。
    await waitFor(() => expect(screen.getByText('server.properties')).toBeInTheDocument())
    expect(screen.getByText('config')).toBeInTheDocument()
    expect(screen.getByText('mods')).toBeInTheDocument()

    await user.click(screen.getByText('server.properties'))
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith('ch1', 'sha-cfg')
      expect(container.querySelector('.cm-content')?.textContent).toContain('motd=Hello')
    })
  })

  it('二进制制品 → 降级占位 + 下载兜底（点下载触发管理面下载端点）', async () => {
    fetchMock.mockResolvedValue({ kind: 'binary', size: 1024, codec: 'none' })
    const user = userEvent.setup()
    render(<FileBrowser source={clientDistSource('ch1', FILES)} />)

    await user.click(await screen.findByText('foo.jar'))
    await waitFor(() => expect(screen.getByText(/二进制文件/)).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: /下载/ }))
    expect(downloadMock).toHaveBeenCalledWith('ch1', 'sha-foo', 'foo.jar')
  })
})
