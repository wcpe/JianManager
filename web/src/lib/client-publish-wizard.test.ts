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
  type TreeFile,
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
