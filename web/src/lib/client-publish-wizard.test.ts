import { describe, it, expect } from 'vitest'
import {
  isDraftPathValid,
  allPathsValid,
  parseManagedDirs,
  canAdvance,
  canPublish,
  nextStep,
  prevStep,
  PUBLISH_STEPS,
  normalizeManifestPath,
  isZipFilename,
  hasPublishDraft,
  buildFileTree,
  collectEntries,
  localDedupKey,
  dedupUnits,
  batchProgressBytes,
  moveFileToDir,
  moveDirToDir,
  collectSubtreeFiles,
  isSelfOrDescendant,
  collectAllDirPaths,
  isCleanAll,
  CLEAN_ALL_SENTINEL,
  buildFileTreeWithDirs,
  renamePathSegment,
  nextUniqueName,
  keepBothPath,
  detectConflicts,
  type TreeFile,
  type FileSystemEntryLike,
} from './client-publish-wizard'

/** 发布版本向导分步逻辑（FR-187）。 */
describe('isDraftPathValid', () => {
  it('合法相对路径通过', () => {
    expect(isDraftPathValid('mods/a.jar')).toBe(true)
    expect(isDraftPathValid('config/foo.toml')).toBe(true)
  })
  it('空 / 绝对路径 / 含 .. 越界拒绝', () => {
    expect(isDraftPathValid('')).toBe(false)
    expect(isDraftPathValid('   ')).toBe(false)
    expect(isDraftPathValid('/etc/passwd')).toBe(false)
    expect(isDraftPathValid('../escape')).toBe(false)
    expect(isDraftPathValid('mods/../../x')).toBe(false)
  })
})

describe('allPathsValid', () => {
  it('空列表不合法（无可发布内容）', () => {
    expect(allPathsValid([])).toBe(false)
  })
  it('任一非法即整体不合法', () => {
    expect(allPathsValid(['mods/a.jar', '/abs'])).toBe(false)
    expect(allPathsValid(['mods/a.jar', 'config/b.toml'])).toBe(true)
  })
})

describe('parseManagedDirs', () => {
  it('逗号/换行分隔、去重、去结尾斜杠、去空项', () => {
    expect(parseManagedDirs('mods, config\nresourcepacks/, mods')).toEqual([
      'mods',
      'config',
      'resourcepacks',
    ])
    expect(parseManagedDirs('  , ,')).toEqual([])
  })
})

describe('canAdvance', () => {
  const base = { draftCount: 1, paths: ['mods/a.jar'], uploading: false }
  it('上传中任何步都不能前进', () => {
    expect(canAdvance('files', { ...base, uploading: true })).toBe(false)
  })
  it('files 步需至少一个文件', () => {
    expect(canAdvance('files', { ...base, draftCount: 0 })).toBe(false)
    expect(canAdvance('files', base)).toBe(true)
  })
  it('configure 步需所有路径合法', () => {
    expect(canAdvance('configure', { ...base, paths: ['/bad'] })).toBe(false)
    expect(canAdvance('configure', base)).toBe(true)
  })
  it('meta 步无额外门槛', () => {
    expect(canAdvance('meta', base)).toBe(true)
  })
  it('review 步无下一步', () => {
    expect(canAdvance('review', base)).toBe(false)
  })
})

describe('canPublish', () => {
  it('有文件 + 路径全合法 + 非上传中才可发布', () => {
    expect(canPublish({ draftCount: 1, paths: ['mods/a.jar'], uploading: false })).toBe(true)
    expect(canPublish({ draftCount: 0, paths: [], uploading: false })).toBe(false)
    expect(canPublish({ draftCount: 1, paths: ['/bad'], uploading: false })).toBe(false)
    expect(canPublish({ draftCount: 1, paths: ['mods/a.jar'], uploading: true })).toBe(false)
  })
})

describe('nextStep / prevStep', () => {
  it('在固定顺序内前后移动，端点钳制', () => {
    expect(nextStep('files')).toBe('configure')
    expect(nextStep('review')).toBe('review')
    expect(prevStep('configure')).toBe('files')
    expect(prevStep('files')).toBe('files')
    expect(PUBLISH_STEPS).toEqual(['files', 'configure', 'meta', 'review'])
  })
})

/** FR-191：zip 路径归一 / 文件树 / 草稿 dirty 判定。 */
describe('normalizeManifestPath', () => {
  it('反斜杠转正斜杠（Windows zip entry）', () => {
    expect(normalizeManifestPath('mods\\fabric\\a.jar')).toBe('mods/fabric/a.jar')
  })
  it('剥离前导 ./ 与 /、压缩重复斜杠、去首尾空白', () => {
    expect(normalizeManifestPath('./mods/a.jar')).toBe('mods/a.jar')
    expect(normalizeManifestPath('/mods/a.jar')).toBe('mods/a.jar')
    expect(normalizeManifestPath('mods//config///a.jar')).toBe('mods/config/a.jar')
    expect(normalizeManifestPath('  mods/a.jar  ')).toBe('mods/a.jar')
  })
  it('剥离多层前导 ./ 段', () => {
    expect(normalizeManifestPath('././mods/a.jar')).toBe('mods/a.jar')
  })
  it('保留中间的合法段（不解析 ..，越界交给 isDraftPathValid 拦）', () => {
    expect(normalizeManifestPath('config/sub/x.toml')).toBe('config/sub/x.toml')
  })
  it('空 / 纯斜杠归一为空串', () => {
    expect(normalizeManifestPath('')).toBe('')
    expect(normalizeManifestPath('/')).toBe('')
    expect(normalizeManifestPath('   ')).toBe('')
  })
})

describe('isZipFilename', () => {
  it('识别 .zip（大小写不敏感）', () => {
    expect(isZipFilename('pack.zip')).toBe(true)
    expect(isZipFilename('PACK.ZIP')).toBe(true)
    expect(isZipFilename('a.b.zip')).toBe(true)
  })
  it('非 zip 拒绝', () => {
    expect(isZipFilename('mod.jar')).toBe(false)
    expect(isZipFilename('config.toml')).toBe(false)
    expect(isZipFilename('zipper.txt')).toBe(false)
    expect(isZipFilename('')).toBe(false)
  })
})

describe('hasPublishDraft', () => {
  it('已上传文件即视为有草稿（非空即 dirty）', () => {
    expect(hasPublishDraft(0)).toBe(false)
    expect(hasPublishDraft(1)).toBe(true)
    expect(hasPublishDraft(5)).toBe(true)
  })
})

/** 构造一个最小 TreeFile（仅文件树需要的字段）。 */
function tf(path: string, index: number, over: Partial<TreeFile> = {}): TreeFile {
  return {
    index,
    path,
    name: path.split('/').pop() ?? path,
    sync: 'strict',
    platform: '',
    size: 1024,
    locked: true,
    ...over,
  }
}

describe('buildFileTree', () => {
  it('空列表 → 空根', () => {
    const root = buildFileTree([])
    expect(root.dirs).toEqual([])
    expect(root.files).toEqual([])
  })

  it('按 / 分段构目录树，叶为文件、枝为目录', () => {
    const root = buildFileTree([
      tf('mods/a.jar', 0),
      tf('mods/b.jar', 1),
      tf('config/foo/x.toml', 2),
      tf('options.txt', 3),
    ])
    // 根级：两个目录 + 一个散文件
    expect(root.dirs.map((d) => d.name)).toEqual(['config', 'mods'])
    expect(root.files.map((f) => f.name)).toEqual(['options.txt'])

    const mods = root.dirs.find((d) => d.name === 'mods')!
    expect(mods.path).toBe('mods')
    expect(mods.files.map((f) => f.name)).toEqual(['a.jar', 'b.jar'])
    expect(mods.dirs).toEqual([])

    const config = root.dirs.find((d) => d.name === 'config')!
    expect(config.dirs.map((d) => d.name)).toEqual(['foo'])
    const foo = config.dirs[0]
    expect(foo.path).toBe('config/foo')
    expect(foo.files.map((f) => f.name)).toEqual(['x.toml'])
  })

  it('目录在前、文件在后，各自字母序', () => {
    const root = buildFileTree([
      tf('z.txt', 0),
      tf('beta/1.jar', 1),
      tf('a.txt', 2),
      tf('alpha/1.jar', 3),
    ])
    expect(root.dirs.map((d) => d.name)).toEqual(['alpha', 'beta'])
    expect(root.files.map((f) => f.name)).toEqual(['a.txt', 'z.txt'])
  })

  it('文件携带回源数组下标（供编排定位 patch/remove）', () => {
    // index = 输入数组中的位置（组件按此 patch drafts[i]），与文件名/嵌套无关。
    const root = buildFileTree([tf('readme.txt', 0), tf('mods/a.jar', 1), tf('mods/b.jar', 2)])
    const mods = root.dirs.find((d) => d.name === 'mods')!
    const byName = Object.fromEntries(mods.files.map((f) => [f.name, f.index]))
    expect(byName['a.jar']).toBe(1)
    expect(byName['b.jar']).toBe(2)
    expect(root.files[0].index).toBe(0)
  })

  it('聚合目录的文件数与字节数（递归含子目录）', () => {
    const root = buildFileTree([
      tf('mods/a.jar', 0, { size: 100 }),
      tf('mods/sub/b.jar', 1, { size: 200 }),
      tf('readme.txt', 2, { size: 50 }),
    ])
    const mods = root.dirs.find((d) => d.name === 'mods')!
    expect(mods.fileCount).toBe(2)
    expect(mods.totalSize).toBe(300)
    // 根聚合包含所有文件
    expect(root.fileCount).toBe(3)
    expect(root.totalSize).toBe(350)
  })

  it('空段路径（前导/尾随斜杠）不产生空名目录', () => {
    const root = buildFileTree([tf('mods/a.jar', 0)])
    // 直接喂已含多余斜杠的 path 也不崩（路径应先经 normalizeManifestPath，但防御性处理）
    const root2 = buildFileTree([tf('mods//a.jar', 1)])
    expect(root.dirs[0].name).toBe('mods')
    expect(root2.dirs[0].name).toBe('mods')
    expect(root2.dirs[0].files.map((f) => f.name)).toEqual(['a.jar'])
  })
})

/** FR-250：目录 entry 递归收集。 */
/** 构造一个文件 entry（webkitGetAsEntry 形态）。 */
function fileEntry(fullPath: string, size = 10): FileSystemEntryLike {
  const name = fullPath.split('/').pop() ?? fullPath
  return {
    isFile: true,
    isDirectory: false,
    fullPath,
    file: async () => new File([new Uint8Array(size)], name),
  }
}
/** 构造一个目录 entry（子项经 readEntries 返回）。 */
function dirEntry(fullPath: string, children: FileSystemEntryLike[]): FileSystemEntryLike {
  return {
    isFile: false,
    isDirectory: true,
    fullPath,
    readEntries: async () => children,
  }
}

describe('collectEntries', () => {
  it('散文件 entry → 单元，path 取归一后的 fullPath', async () => {
    const units = await collectEntries([fileEntry('/a.jar', 5)])
    expect(units).toHaveLength(1)
    expect(units[0].path).toBe('a.jar')
    expect(units[0].file.size).toBe(5)
    expect(units[0].file.name).toBe('a.jar')
  })

  it('目录 entry 递归下钻，保留相对目录结构（深度优先前序）', async () => {
    // /pack 下：mods/a.jar、mods/sub/b.jar、options.txt
    const tree = [
      dirEntry('/pack', [
        dirEntry('/pack/mods', [
          fileEntry('/pack/mods/a.jar'),
          dirEntry('/pack/mods/sub', [fileEntry('/pack/mods/sub/b.jar')]),
        ]),
        fileEntry('/pack/options.txt'),
      ]),
    ]
    const units = await collectEntries(tree)
    expect(units.map((u) => u.path)).toEqual([
      'pack/mods/a.jar',
      'pack/mods/sub/b.jar',
      'pack/options.txt',
    ])
  })

  it('跳过 .DS_Store 与 __MACOSX/ 噪音', async () => {
    const units = await collectEntries([
      dirEntry('/p', [
        fileEntry('/p/a.jar'),
        fileEntry('/p/.DS_Store'),
        fileEntry('/__MACOSX/foo'),
      ]),
    ])
    expect(units.map((u) => u.path)).toEqual(['p/a.jar'])
  })

  it('散文件 + 文件夹混合森林累加', async () => {
    const units = await collectEntries([
      fileEntry('/loose.txt'),
      dirEntry('/d', [fileEntry('/d/x.jar')]),
    ])
    expect(units.map((u) => u.path)).toEqual(['loose.txt', 'd/x.jar'])
  })
})

describe('localDedupKey / dedupUnits', () => {
  it('去重键按 name + size', () => {
    expect(localDedupKey('a.jar', 100)).toBe(localDedupKey('a.jar', 100))
    expect(localDedupKey('a.jar', 100)).not.toBe(localDedupKey('a.jar', 101))
    expect(localDedupKey('a.jar', 100)).not.toBe(localDedupKey('b.jar', 100))
  })

  it('同 name+size 只保留首个进 unique，keys 记每个原单元的键', () => {
    const units = [
      { name: 'a.jar', size: 100 },
      { name: 'b.jar', size: 200 },
      { name: 'a.jar', size: 100 }, // 与 [0] 同键 → 去重
    ]
    const plan = dedupUnits(units, (u) => u)
    expect(plan.unique).toHaveLength(2)
    expect(plan.unique.map((u) => u.name)).toEqual(['a.jar', 'b.jar'])
    // keys 与输入等长，第 2 项键 = 第 0 项键（发布时据此复用上传结果）。
    expect(plan.keys).toHaveLength(3)
    expect(plan.keys[2]).toBe(plan.keys[0])
    expect(plan.keys[1]).not.toBe(plan.keys[0])
  })

  it('同 name 不同 size 不去重（视为不同内容）', () => {
    const plan = dedupUnits(
      [
        { name: 'a.jar', size: 100 },
        { name: 'a.jar', size: 101 },
      ],
      (u) => u,
    )
    expect(plan.unique).toHaveLength(2)
  })
})

describe('batchProgressBytes', () => {
  it('累计 base + 当前文件已传', () => {
    expect(batchProgressBytes(1000, 500, 5000)).toBe(1500)
  })
  it('夹取不超过总字节', () => {
    expect(batchProgressBytes(4800, 500, 5000)).toBe(5000)
    expect(batchProgressBytes(5000, 0, 5000)).toBe(5000)
  })
})

/** FR-254：文件树拖拽编排。 */
describe('moveFileToDir', () => {
  it('移到目录下：拼为目标目录 + 文件名', () => {
    expect(moveFileToDir('mods/a.jar', 'config')).toBe('config/a.jar')
    expect(moveFileToDir('mods/sub/a.jar', 'config')).toBe('config/a.jar')
  })
  it('移到根：仅留文件名', () => {
    expect(moveFileToDir('mods/a.jar', '')).toBe('a.jar')
    expect(moveFileToDir('mods/sub/a.jar', '')).toBe('a.jar')
  })
  it('已在目标目录下 → 路径不变', () => {
    expect(moveFileToDir('config/a.jar', 'config')).toBe('config/a.jar')
  })
  it('目标目录尾随斜杠被归一', () => {
    expect(moveFileToDir('mods/a.jar', 'config/')).toBe('config/a.jar')
    expect(moveFileToDir('mods/a.jar', '  config  ')).toBe('config/a.jar')
  })
})

describe('moveDirToDir', () => {
  it('移到目录下：拼为目标目录 + 源目录名', () => {
    expect(moveDirToDir('config/foo', 'mods')).toBe('mods/foo')
  })
  it('移到根：仅留源目录名', () => {
    expect(moveDirToDir('config/foo', '')).toBe('foo')
  })
  it('源等于目标 → no-op', () => {
    expect(moveDirToDir('config/foo', 'config')).toBe('config/foo')
  })
})

describe('collectSubtreeFiles', () => {
  it('递归收集子目录全部文件（含嵌套）', () => {
    const root = buildFileTree([
      tf('mods/a.jar', 0),
      tf('mods/sub/b.jar', 1),
      tf('mods/sub/deep/c.jar', 2),
      tf('readme.txt', 3),
    ])
    const mods = root.dirs.find((d) => d.name === 'mods')!
    const files = collectSubtreeFiles(mods)
    expect(files.map((f) => f.path).sort()).toEqual([
      'mods/a.jar',
      'mods/sub/b.jar',
      'mods/sub/deep/c.jar',
    ])
    expect(files.map((f) => f.index).sort()).toEqual([0, 1, 2])
  })
})

describe('isSelfOrDescendant', () => {
  it('自身 → true', () => {
    expect(isSelfOrDescendant('mods', 'mods')).toBe(true)
    expect(isSelfOrDescendant('mods/sub', 'mods/sub')).toBe(true)
  })
  it('后代 → true', () => {
    expect(isSelfOrDescendant('mods', 'mods/sub')).toBe(true)
    expect(isSelfOrDescendant('mods', 'mods/sub/deep')).toBe(true)
  })
  it('非后代 → false', () => {
    expect(isSelfOrDescendant('mods', 'config')).toBe(false)
    expect(isSelfOrDescendant('mods', 'modsxyz')).toBe(false)
  })
})

/** FR-255：从草稿文件派生目录路径（managedDirs 目录树勾选用）。 */
describe('collectAllDirPaths', () => {
  it('空列表 → 空数组', () => {
    expect(collectAllDirPaths([])).toEqual([])
  })
  it('根目录散文件不产生目录', () => {
    expect(collectAllDirPaths([tf('options.txt', 0)])).toEqual([])
  })
  it('逐层收集所有目录前缀（含深层嵌套），按字母序', () => {
    expect(
      collectAllDirPaths([
        tf('mods/a.jar', 0),
        tf('mods/sub/b.jar', 1),
        tf('config/foo/x.toml', 2),
        tf('options.txt', 3),
      ]),
    ).toEqual(['config', 'config/foo', 'mods', 'mods/sub'])
  })
  it('去重相同目录前缀', () => {
    expect(
      collectAllDirPaths([
        tf('mods/a.jar', 0),
        tf('mods/b.jar', 1),
        tf('mods/sub/c.jar', 2),
      ]),
    ).toEqual(['mods', 'mods/sub'])
  })
  it('路径归一（反斜杠 / 前导 ./）', () => {
    expect(
      collectAllDirPaths([tf('./mods\\sub/a.jar', 0)]),
    ).toEqual(['mods', 'mods/sub'])
  })
})

/** FR-255：clean-all 哨兵判定。 */
describe('isCleanAll', () => {
  it('含 "*" 哨兵 → true', () => {
    expect(isCleanAll(['*'])).toBe(true)
    expect(isCleanAll(['mods', '*'])).toBe(true)
  })
  it('不含哨兵 → false', () => {
    expect(isCleanAll([])).toBe(false)
    expect(isCleanAll(['mods', 'config'])).toBe(false)
  })
  it('哨兵常量', () => {
    expect(CLEAN_ALL_SENTINEL).toBe('*')
  })
})

/** FR-261：文件资源管理器纯函数。 */
describe('buildFileTreeWithDirs', () => {
  it('空目录插入树中显示为空目录节点', () => {
    const root = buildFileTreeWithDirs(
      [tf('mods/a.jar', 0)],
      ['config'],
    )
    expect(root.dirs.map((d) => d.name)).toEqual(['config', 'mods'])
    const config = root.dirs.find((d) => d.name === 'config')!
    expect(config.files).toEqual([])
    expect(config.dirs).toEqual([])
    expect(config.fileCount).toBe(0)
  })

  it('已存在的目录不重复创建（合并）', () => {
    const root = buildFileTreeWithDirs(
      [tf('mods/a.jar', 0)],
      ['mods'],
    )
    expect(root.dirs).toHaveLength(1)
    expect(root.dirs[0].name).toBe('mods')
    expect(root.dirs[0].files.map((f) => f.name)).toEqual(['a.jar'])
  })

  it('嵌套空目录逐层创建', () => {
    const root = buildFileTreeWithDirs(
      [tf('readme.txt', 0)],
      ['mods/sub/deep'],
    )
    const mods = root.dirs.find((d) => d.name === 'mods')!
    expect(mods.dirs.map((d) => d.name)).toEqual(['sub'])
    const sub = mods.dirs[0]
    expect(sub.dirs.map((d) => d.name)).toEqual(['deep'])
    expect(sub.dirs[0].files).toEqual([])
  })

  it('空 emptyDirs 退化为普通 buildFileTree', () => {
    const root = buildFileTreeWithDirs([tf('mods/a.jar', 0)], [])
    expect(root.dirs.map((d) => d.name)).toEqual(['mods'])
  })
})

describe('renamePathSegment', () => {
  it('替换末段文件名，保留目录前缀', () => {
    expect(renamePathSegment('mods/sub/a.jar', 'b.jar')).toBe('mods/sub/b.jar')
  })
  it('根级文件仅替换文件名', () => {
    expect(renamePathSegment('a.jar', 'b.jar')).toBe('b.jar')
  })
  it('路径归一（反斜杠/前导斜杠）', () => {
    expect(renamePathSegment('/mods\\a.jar', 'b.jar')).toBe('mods/b.jar')
  })
})

describe('nextUniqueName', () => {
  it('base 不冲突 → 返回 base', () => {
    expect(nextUniqueName('新建文件夹', ['mods', 'config'])).toBe('新建文件夹')
  })
  it('base 冲突 → 加数字后缀', () => {
    expect(nextUniqueName('新建文件夹', ['新建文件夹'])).toBe('新建文件夹 2')
    expect(nextUniqueName('新建文件夹', ['新建文件夹', '新建文件夹 2'])).toBe('新建文件夹 3')
  })
  it('空 existingNames → 返回 base', () => {
    expect(nextUniqueName('foo', [])).toBe('foo')
  })
})

describe('keepBothPath', () => {
  it('有扩展名 → stem 与扩展名间插 (1)', () => {
    expect(keepBothPath('mods/a.jar', ['mods/a.jar'])).toBe('mods/a (1).jar')
  })
  it('递增直到不冲突', () => {
    expect(keepBothPath('mods/a.jar', ['mods/a.jar', 'mods/a (1).jar'])).toBe('mods/a (2).jar')
  })
  it('无扩展名 → 后缀直接追加', () => {
    expect(keepBothPath('readme', ['readme'])).toBe('readme (1)')
  })
  it('根级文件（无目录前缀）', () => {
    expect(keepBothPath('a.jar', ['a.jar'])).toBe('a (1).jar')
  })
  it('不冲突时仍加 (1)（调用方应先检测冲突）', () => {
    expect(keepBothPath('mods/a.jar', [])).toBe('mods/a (1).jar')
  })
})

describe('detectConflicts', () => {
  it('返回 incoming 中与 existing 同路径的项', () => {
    expect(detectConflicts(['a.jar', 'b.jar'], ['a.jar'])).toEqual(['a.jar'])
  })
  it('incoming 去重', () => {
    expect(detectConflicts(['a.jar', 'a.jar'], ['a.jar'])).toEqual(['a.jar'])
  })
  it('无冲突 → 空数组', () => {
    expect(detectConflicts(['a.jar'], ['b.jar'])).toEqual([])
  })
})
