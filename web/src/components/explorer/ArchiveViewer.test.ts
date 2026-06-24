import { describe, it, expect } from 'vitest'
import { buildEntryTree } from './archive-tree'
import type { ArchiveEntry } from '@/api/archive'

/** 构造归档条目；目录条目 name 以「/」结尾、isDir=true（与 Worker 列举一致）。 */
function entry(name: string, isDir = false, size = 0): ArchiveEntry {
  return { name, isDir, size, compressedSize: size, modified: 0, crc32: 0 }
}

/** 收集某一层全部节点的 label（按出现顺序）。 */
function labels(nodes: ReturnType<typeof buildEntryTree>): string[] {
  return nodes.map((n) => n.label)
}

describe('buildEntryTree（BUG-010 归档树重建）', () => {
  it('目录条目与其下文件条目混合时，同名顶级目录唯一（不重复）', () => {
    // 真 Paper server.jar 的典型布局：zip 同时含目录条目（io/）与文件条目（io/papermc/...）。
    const tree = buildEntryTree([
      entry('io/', true),
      entry('io/papermc/Main.class'),
      entry('META-INF/', true),
      entry('META-INF/MANIFEST.MF'),
      entry('paperclip/', true),
      entry('paperclip/Loader.class'),
    ])

    const top = labels(tree)
    // 顺序按条目插入序、不强求；重点是去重——三个顶级目录各唯一。
    expect([...top].sort()).toEqual(['META-INF', 'io', 'paperclip'])
    // 每个顶级目录只出现一次（修复前 io/META-INF/paperclip 各出现 2 次）。
    expect(top.filter((l) => l === 'io')).toHaveLength(1)
    expect(top.filter((l) => l === 'META-INF')).toHaveLength(1)
    expect(top.filter((l) => l === 'paperclip')).toHaveLength(1)
  })

  it('目录条目在文件条目之前/之后到达都合并为同一节点', () => {
    // 文件条目先到（隐式建 a/），目录条目 a/ 后到，应复用同一节点而非新建。
    const treeFileFirst = buildEntryTree([
      entry('a/x.txt'),
      entry('a/', true),
    ])
    expect(treeFileFirst).toHaveLength(1)
    expect(treeFileFirst[0].label).toBe('a')
    expect(treeFileFirst[0].children.map((c) => c.label)).toEqual(['x.txt'])

    // 目录条目先到，文件条目后到，同样唯一。
    const treeDirFirst = buildEntryTree([
      entry('a/', true),
      entry('a/x.txt'),
    ])
    expect(treeDirFirst).toHaveLength(1)
    expect(treeDirFirst[0].children.map((c) => c.label)).toEqual(['x.txt'])
  })

  it('深层嵌套目录条目与文件派生的中间目录合并', () => {
    const tree = buildEntryTree([
      entry('com/', true),
      entry('com/foo/', true),
      entry('com/foo/bar/Baz.class'),
      entry('com/foo/Qux.class'),
    ])
    // com 唯一
    expect(tree).toHaveLength(1)
    expect(tree[0].label).toBe('com')
    // com/foo 唯一（目录条目 com/foo/ 与文件派生的 com/foo 合并）
    const foo = tree[0].children.filter((c) => c.label === 'foo')
    expect(foo).toHaveLength(1)
    // com/foo 下：bar 目录 + Qux.class 文件（目录排前）
    expect(foo[0].children.map((c) => c.label)).toEqual(['bar', 'Qux.class'])
  })

  it('目录排在文件前、同层按名排序', () => {
    const tree = buildEntryTree([
      entry('z.txt'),
      entry('plugins/', true),
      entry('a.yml'),
    ])
    expect(labels(tree)).toEqual(['plugins', 'a.yml', 'z.txt'])
  })

  it('根目录条目与空条目被忽略', () => {
    const tree = buildEntryTree([entry('/', true), entry(''), entry('only.txt')])
    expect(labels(tree)).toEqual(['only.txt'])
  })
})
