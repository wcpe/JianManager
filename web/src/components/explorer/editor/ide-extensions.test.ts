import { describe, it, expect } from 'vitest'
import {
  copyLineDown,
  copyLineUp,
  deleteLine,
  moveLineDown,
  moveLineUp,
  selectLine,
  toggleComment,
  toggleBlockComment,
} from '@codemirror/commands'
import { ideKeymap } from './ide-extensions'

describe('ideKeymap', () => {
  const bindings = ideKeymap()
  const keys = bindings.map((b) => b.key)

  it('never binds the save shortcut (Mod-s) so Ctrl+S stays the history-save key', () => {
    for (const k of keys) {
      expect(k).toBeDefined()
      // 不得包含独立的 Mod-s（保存键由 FR-070 CodeEditor 拦截）。
      expect(k).not.toBe('Mod-s')
      expect(k).not.toBe('Ctrl-s')
      expect(k).not.toBe('Cmd-s')
    }
  })

  it('has no duplicate key bindings', () => {
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('binds line operations and comment commands to the expected CodeMirror commands', () => {
    const byKey = (k: string) => bindings.find((b) => b.key === k)?.run
    expect(byKey('Mod-/')).toBe(toggleComment)
    expect(byKey('Shift-Alt-a')).toBe(toggleBlockComment)
    expect(byKey('Shift-Mod-k')).toBe(deleteLine)
    expect(byKey('Shift-Mod-d')).toBe(copyLineDown)
    expect(byKey('Shift-Mod-Alt-d')).toBe(copyLineUp)
    expect(byKey('Alt-ArrowDown')).toBe(moveLineDown)
    expect(byKey('Alt-ArrowUp')).toBe(moveLineUp)
    expect(byKey('Mod-l')).toBe(selectLine)
  })
})
