import { describe, it, expect, vi, beforeEach } from 'vitest'

// hoisted mock：暴露内容/下载 spy，断言适配器对管理面端点的调用与映射（FR-214）。
const { fetchMock, downloadMock } = vi.hoisted(() => ({
  fetchMock: vi.fn(),
  downloadMock: vi.fn(),
}))
vi.mock('@/api/clientVersions', () => ({
  fetchClientArtifactContent: fetchMock,
  downloadClientArtifact: downloadMock,
}))

import { clientDistSource, manifestFilesToDistFiles, type ClientDistFile } from './clientDistSource'
import type { ManifestFile } from '@/api/clientVersions'

const FILES: ClientDistFile[] = [
  { path: 'mods/foo.jar', size: 1024, artifactSha: 'sha-foo' },
  { path: 'config/server.properties', size: 50, artifactSha: 'sha-cfg' },
  { path: 'config/ignored.txt', size: 0, artifactSha: '' }, // 无制品（如 sync=ignore 占位）
]

describe('clientDistSource 映射（FR-214）', () => {
  beforeEach(() => {
    fetchMock.mockReset()
    downloadMock.mockReset()
  })

  it('flat=true 且 list 一次返回全部条目（path/name/size，均为文件）', async () => {
    const src = clientDistSource('ch1', FILES)
    expect(src.flat).toBe(true)
    const entries = await src.list('')
    expect(entries).toHaveLength(3)
    expect(entries[0]).toMatchObject({ path: 'mods/foo.jar', name: 'foo.jar', isDir: false, size: 1024 })
    expect(entries[1]).toMatchObject({ path: 'config/server.properties', name: 'server.properties', isDir: false })
    // 全部为文件（扁平 manifest 无显式目录条目，目录由 buildTree 内部推导）。
    expect(entries.every((e) => !e.isDir)).toBe(true)
  })

  it('readContent 文本 → 经制品 sha 调内容端点并透传文本', async () => {
    fetchMock.mockResolvedValue({ kind: 'text', content: 'motd=Hi', size: 50, codec: 'none' })
    const src = clientDistSource('ch1', FILES)
    const res = await src.readContent({ path: 'config/server.properties', name: 'server.properties', isDir: false })
    expect(fetchMock).toHaveBeenCalledWith('ch1', 'sha-cfg')
    expect(res).toEqual({ kind: 'text', content: 'motd=Hi' })
  })

  it('readContent 二进制/超大降级按 kind 透传（不带内容）', async () => {
    const src = clientDistSource('ch1', FILES)

    fetchMock.mockResolvedValueOnce({ kind: 'binary', size: 1024, codec: 'none' })
    const bin = await src.readContent({ path: 'mods/foo.jar', name: 'foo.jar', isDir: false })
    expect(bin).toEqual({ kind: 'binary' })

    fetchMock.mockResolvedValueOnce({ kind: 'too-large', size: 9_000_000, codec: 'none' })
    const big = await src.readContent({ path: 'mods/foo.jar', name: 'foo.jar', isDir: false })
    expect(big).toEqual({ kind: 'too-large', size: 9_000_000 })
  })

  it('readContent 缺制品 sha → 错误占位且不调端点', async () => {
    const src = clientDistSource('ch1', FILES)
    const res = await src.readContent({ path: 'config/ignored.txt', name: 'ignored.txt', isDir: false })
    expect(res.kind).toBe('error')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('download 经制品 sha 调下载端点、文件名取 path 末段', () => {
    const src = clientDistSource('ch1', FILES)
    src.download?.({ path: 'mods/foo.jar', name: 'foo.jar', isDir: false })
    expect(downloadMock).toHaveBeenCalledWith('ch1', 'sha-foo', 'foo.jar')
  })

  it('download 缺制品 sha → 不触发下载', () => {
    const src = clientDistSource('ch1', FILES)
    src.download?.({ path: 'config/ignored.txt', name: 'ignored.txt', isDir: false })
    expect(downloadMock).not.toHaveBeenCalled()
  })

  it('未知 path 的条目 readContent → 错误占位（防御越界）', async () => {
    const src = clientDistSource('ch1', FILES)
    const res = await src.readContent({ path: 'does/not/exist', name: 'exist', isDir: false })
    expect(res.kind).toBe('error')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('manifestFilesToDistFiles 映射（FR-214）', () => {
  it('取 artifact.sha256 作内容寻址 key，保留 path/size', () => {
    const manifest: ManifestFile[] = [
      {
        path: 'mods/a.jar',
        sha256: 'raw-a',
        md5: 'm',
        size: 123,
        sync: 'strict',
        platform: '',
        artifact: { sha256: 'art-a', size: 100, codec: 'none' },
      },
    ]
    expect(manifestFilesToDistFiles(manifest)).toEqual([{ path: 'mods/a.jar', size: 123, artifactSha: 'art-a' }])
  })

  it('artifact 缺失 → artifactSha 回退空串（不崩）', () => {
    const manifest = [
      { path: 'x', sha256: 's', md5: 'm', size: 1, sync: 'ignore', platform: '' },
    ] as unknown as ManifestFile[]
    expect(manifestFilesToDistFiles(manifest)).toEqual([{ path: 'x', size: 1, artifactSha: '' }])
  })
})
