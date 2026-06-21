import { describe, it, expect } from 'vitest'
import { planPaste, cutEntries, copyEntries, type Clipboard } from './clipboard'

const file = (path: string): { path: string; isDir: boolean } => ({ path, isDir: false })
const dir = (path: string): { path: string; isDir: boolean } => ({ path, isDir: true })

describe('planPaste — cut (move)', () => {
  it('moves files and dirs via rename to target dir', () => {
    const clip = cutEntries([file('a.txt'), dir('plugins')])
    const plan = planPaste(clip, 'sub', new Set())
    expect(plan.ops).toEqual([
      { kind: 'move', from: 'a.txt', to: 'sub/a.txt' },
      { kind: 'move', from: 'plugins', to: 'sub/plugins' },
    ])
    expect(plan.skipped).toEqual([])
  })

  it('skips moving a dir into itself or its subdir', () => {
    const clip = cutEntries([dir('plugins')])
    const plan = planPaste(clip, 'plugins/sub', new Set())
    expect(plan.ops).toEqual([])
    expect(plan.skipped).toEqual([{ path: 'plugins', reason: 'into-self' }])
  })

  it('skips when target equals source directory', () => {
    const clip = cutEntries([file('dir/a.txt')])
    const plan = planPaste(clip, 'dir', new Set())
    expect(plan.ops).toEqual([])
    expect(plan.skipped).toEqual([{ path: 'dir/a.txt', reason: 'same-dir' }])
  })
})

describe('planPaste — copy', () => {
  it('copies files via read+write', () => {
    const clip = copyEntries([file('a.txt')])
    const plan = planPaste(clip, 'sub', new Set())
    expect(plan.ops).toEqual([{ kind: 'copy', from: 'a.txt', to: 'sub/a.txt' }])
  })

  it('refuses to copy directories (out of scope)', () => {
    const clip = copyEntries([dir('plugins')])
    const plan = planPaste(clip, 'sub', new Set())
    expect(plan.ops).toEqual([])
    expect(plan.skipped).toEqual([{ path: 'plugins', reason: 'dir-copy-unsupported' }])
  })
})

describe('planPaste — conflicts & empties', () => {
  it('skips entries whose name already exists in target', () => {
    const clip = copyEntries([file('a.txt'), file('b.txt')])
    const plan = planPaste(clip, 'sub', new Set(['a.txt']))
    expect(plan.ops).toEqual([{ kind: 'copy', from: 'b.txt', to: 'sub/b.txt' }])
    expect(plan.skipped).toEqual([{ path: 'a.txt', reason: 'name-conflict' }])
  })

  it('returns empty plan for null/empty clipboard', () => {
    expect(planPaste(null, 'sub', new Set())).toEqual({ ops: [], skipped: [] })
    const empty: Clipboard = { mode: 'cut', entries: [] }
    expect(planPaste(empty, 'sub', new Set())).toEqual({ ops: [], skipped: [] })
  })

  it('moves a root-level file into a subdir', () => {
    const clip = cutEntries([file('server.properties')])
    const plan = planPaste(clip, 'backup', new Set())
    expect(plan.ops).toEqual([
      { kind: 'move', from: 'server.properties', to: 'backup/server.properties' },
    ])
  })
})
