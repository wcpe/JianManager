import { describe, it, expect } from 'vitest'
import { buildTree } from './tree'
import type { FileEntry } from './types'

/** 便捷构造文件条目。 */
function file(path: string, size = 0): FileEntry {
  return { path, name: path.split('/').pop() ?? path, isDir: false, size }
}
/** 便捷构造目录条目。 */
function dir(path: string): FileEntry {
  return { path, name: path.split('/').pop() ?? path, isDir: true }
}

describe('buildTree（共享文件浏览器扁平→层级，FR-213）', () => {
  it('空列表得到空根', () => {
    const root = buildTree([])
    expect(root.path).toBe('')
    expect(root.dirs).toHaveLength(0)
    expect(root.files).toHaveLength(0)
    expect(root.fileCount).toBe(0)
    expect(root.totalSize).toBe(0)
  })

  it('根级散文件直接挂到根', () => {
    const root = buildTree([file('a.txt', 10), file('b.txt', 20)])
    expect(root.dirs).toHaveLength(0)
    expect(root.files.map((f) => f.name)).toEqual(['a.txt', 'b.txt'])
    expect(root.fileCount).toBe(2)
    expect(root.totalSize).toBe(30)
  })

  it('嵌套路径自动补建中间目录', () => {
    const root = buildTree([file('plugins/Essentials/config.yml', 100)])
    expect(root.dirs).toHaveLength(1)
    const plugins = root.dirs[0]
    expect(plugins.name).toBe('plugins')
    expect(plugins.path).toBe('plugins')
    expect(plugins.dirs).toHaveLength(1)
    const ess = plugins.dirs[0]
    expect(ess.name).toBe('Essentials')
    expect(ess.path).toBe('plugins/Essentials')
    expect(ess.files.map((f) => f.name)).toEqual(['config.yml'])
  })

  it('目录在前文件在后、各自字母序', () => {
    const root = buildTree([file('z.txt'), file('a.txt'), dir('mods'), dir('alpha')])
    expect(root.dirs.map((d) => d.name)).toEqual(['alpha', 'mods'])
    expect(root.files.map((f) => f.name)).toEqual(['a.txt', 'z.txt'])
  })

  it('目录聚合递归 fileCount/totalSize', () => {
    const root = buildTree([
      file('world/level.dat', 1000),
      file('world/region/r.0.0.mca', 2000),
      file('server.properties', 50),
    ])
    expect(root.fileCount).toBe(3)
    expect(root.totalSize).toBe(3050)
    const world = root.dirs.find((d) => d.name === 'world')!
    expect(world.fileCount).toBe(2)
    expect(world.totalSize).toBe(3000)
    const region = world.dirs.find((d) => d.name === 'region')!
    expect(region.fileCount).toBe(1)
    expect(region.totalSize).toBe(2000)
  })

  it('显式空目录条目也建节点', () => {
    const root = buildTree([dir('empty')])
    expect(root.dirs.map((d) => d.name)).toEqual(['empty'])
    expect(root.dirs[0].files).toHaveLength(0)
    expect(root.fileCount).toBe(0)
  })

  it('归一反斜杠/前导斜杠/重复斜杠', () => {
    const root = buildTree([file('\\a\\b\\c.txt', 5), file('/x//y.txt', 7)])
    const a = root.dirs.find((d) => d.name === 'a')!
    expect(a.dirs[0].name).toBe('b')
    expect(a.dirs[0].files[0].name).toBe('c.txt')
    const x = root.dirs.find((d) => d.name === 'x')!
    expect(x.files[0].name).toBe('y.txt')
  })

  it('叶节点回传原始 entry（含完整 path 供预览/下载）', () => {
    const e = file('dir/sub/file.json', 42)
    const root = buildTree([e])
    const leaf = root.dirs[0].dirs[0].files[0]
    expect(leaf.entry).toBe(e)
    expect(leaf.entry.path).toBe('dir/sub/file.json')
  })
})
