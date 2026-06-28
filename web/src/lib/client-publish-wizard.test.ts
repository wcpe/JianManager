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
