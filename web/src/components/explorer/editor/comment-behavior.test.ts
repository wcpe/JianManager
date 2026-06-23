/**
 * 批量注释/取消注释的行为级集成测试（FR-073）。
 *
 * 不只断言纯函数表，而是真在 EditorState 上跑 CodeMirror 的 toggleComment：
 * 验证「按文件类型注释符」是经 EditorState.languageData 注入 commentTokens 后
 * 真正生效的——纯文本/yaml 用 `#`、json 用 `//`、html 用块注释 `<!-- -->`。
 *
 * 仅状态层（无 DOM）：toggleComment 通过 {state, dispatch} 目标运行，能覆盖注释逻辑；
 * 依赖视图的行删除/移动命令的「绑定正确性」已在 ide-extensions.test.ts 断言。
 */
import { describe, it, expect } from 'vitest'
import { EditorState, EditorSelection, type Command } from '@codemirror/state'
import { toggleComment } from '@codemirror/commands'
import { languageKindFor } from '../language'
import { commentTokensForFilename } from './comment'

/** 用某文件名对应的注释符建一个光标在行首的最小编辑状态。 */
function stateFor(filename: string, doc: string): EditorState {
  const tokens = commentTokensForFilename(filename, languageKindFor(filename))
  const commentTokens: { line?: string; block?: { open: string; close: string } } = {}
  if (tokens.line) commentTokens.line = tokens.line
  if (tokens.block) commentTokens.block = tokens.block
  return EditorState.create({
    doc,
    selection: EditorSelection.cursor(0),
    extensions: [EditorState.languageData.of(() => [{ commentTokens }])],
  })
}

/** 在状态目标上运行命令，返回结果文档（命令未生效时返回原文档）。 */
function runCommand(cmd: Command, state: EditorState): string {
  let next = state
  // CodeMirror 命令接受最小 {state, dispatch} 目标；行注释逻辑不需要视图。
  const ok = (cmd as unknown as (t: { state: EditorState; dispatch: (tr: { state: EditorState }) => void }) => boolean)(
    {
      state,
      dispatch: (tr) => {
        next = tr.state
      },
    },
  )
  return ok ? next.doc.toString() : state.doc.toString()
}

describe('toggleComment with file-type comment tokens (FR-073)', () => {
  it('comments a plain/yaml line with #', () => {
    expect(runCommand(toggleComment, stateFor('latest.log', 'hello'))).toBe('# hello')
    expect(runCommand(toggleComment, stateFor('config.yml', 'a: 1'))).toBe('# a: 1')
  })

  it('comments a json line with //', () => {
    expect(runCommand(toggleComment, stateFor('data.json', '"k": 1'))).toBe('// "k": 1')
  })

  it('uncomments an already-commented line (round-trip)', () => {
    const commented = runCommand(toggleComment, stateFor('app.properties', 'key=value'))
    expect(commented).toBe('# key=value')
    expect(runCommand(toggleComment, stateFor('app.properties', commented))).toBe('key=value')
  })

  it('uses block comment for html where no line comment exists', () => {
    // html 仅有块注释符，toggleComment 回退到块注释包裹。
    expect(runCommand(toggleComment, stateFor('index.html', 'text'))).toBe('<!-- text -->')
  })
})
