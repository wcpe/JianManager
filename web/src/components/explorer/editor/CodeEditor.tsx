import { useEffect, useRef } from 'react'
import { EditorState, EditorSelection, type Extension } from '@codemirror/state'
import {
  EditorView,
  lineNumbers,
  keymap,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
} from '@codemirror/view'
import {
  defaultKeymap,
  history,
  historyKeymap,
  indentWithTab,
} from '@codemirror/commands'
import {
  syntaxHighlighting,
  defaultHighlightStyle,
  indentOnInput,
  bracketMatching,
} from '@codemirror/language'
import { languageExtensionFor } from '../language'
import { ideExtensions } from './ide-extensions'

/**
 * 共享 CodeMirror 6 编辑器（FR-070 编辑器基础）。
 *
 * 在 FR-008 编辑器之上：
 * - 多格式高亮（yaml/json/properties/ini/toml/...，见 language.ts）；
 * - Ctrl+S / Cmd+S 拦截 → preventDefault 并回调 onSave（接 FR-051 改前快照/版本）；
 * - 行号 / 撤销重做 / 括号匹配 / 自动缩进 / 折行。
 *
 * FR-073 迷你 IDE 增强在此基础上叠加（见 ide-extensions.ts）：搜索/替换面板（Ctrl+F，
 * 含正则/全词/全部替换）、删除一行/复制一行/上下移动一行/选中整行、按文件类型的
 * 批量注释/取消注释，且所有键位避开 Ctrl+S（保存仍走下方 saveKeymap）。
 */
interface CodeEditorProps {
  /** 文档内容。 */
  value: string
  /** 文件名，决定语法高亮语言。 */
  filename: string
  /** 只读（查看态）。 */
  readOnly?: boolean
  /** 编辑回调。 */
  onChange?: (value: string) => void
  /** Ctrl+S / Cmd+S 触发（仅非只读时生效）。返回后由调用方执行保存。 */
  onSave?: () => void
  /**
   * 定位到指定行（1 起），用于搜索结果点击跳转（FR-074）。
   * 值变化时滚动并把光标置于该行。0/undefined 不定位。
   */
  gotoLine?: number
  /**
   * 定位 nonce：与 gotoLine 搭配，变化即重触发定位（即便目标行相同），
   * 用于「再次点击同一搜索命中」也能重新跳转。
   */
  gotoNonce?: number
}

/**
 * 轻量 CodeMirror 编辑器封装。
 * 编辑器实例随 filename/readOnly 变化重建；外部 value 变化（载入新文件）经第二个 effect 同步，不打断输入。
 * onChange/onSave 经 ref 取最新，避免每次回调变化都重建编辑器。
 */
export default function CodeEditor({
  value,
  filename,
  readOnly = false,
  onChange,
  onSave,
  gotoLine,
  gotoNonce,
}: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const onChangeRef = useRef(onChange)
  const onSaveRef = useRef(onSave)

  useEffect(() => {
    onChangeRef.current = onChange
    onSaveRef.current = onSave
  }, [onChange, onSave])

  useEffect(() => {
    if (!hostRef.current) return
    // Ctrl+S/Cmd+S：拦截浏览器默认「保存网页」，触发受控保存（FR-070）。
    const saveKeymap = keymap.of([
      {
        key: 'Mod-s',
        preventDefault: true,
        run: () => {
          onSaveRef.current?.()
          return true
        },
      },
    ])

    const extensions: Extension[] = [
      lineNumbers(),
      history(),
      drawSelection(),
      indentOnInput(),
      bracketMatching(),
      highlightActiveLine(),
      highlightActiveLineGutter(),
      syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
      // 保存键放在默认键位之前，保证 Mod-s 优先被它消费。
      saveKeymap,
      // FR-073 迷你 IDE 增强（搜索/替换、行操作、批量注释）。
      // 置于默认键位之前，保证 Mod-/ 等专属键位优先；其内部不绑定 Mod-s，不与保存冲突。
      ...ideExtensions(filename),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      ...languageExtensionFor(filename),
      EditorState.readOnly.of(readOnly),
      EditorView.editable.of(!readOnly),
      EditorView.lineWrapping,
      EditorView.updateListener.of((u) => {
        if (u.docChanged) onChangeRef.current?.(u.state.doc.toString())
      }),
      EditorView.theme({
        '&': { height: '100%', fontSize: '13px' },
        '.cm-scroller': { fontFamily: 'Consolas, Monaco, monospace', overflow: 'auto' },
        // 搜索/替换面板（FR-073）：紧凑排版，按钮可换行，短宽度面板不溢出。
        '.cm-panels': { fontSize: '12px' },
        '.cm-search': { display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '4px' },
        '.cm-search label': { display: 'inline-flex', alignItems: 'center', gap: '2px' },
      }),
    ]

    const view = new EditorView({
      parent: hostRef.current,
      state: EditorState.create({ doc: value, extensions }),
    })
    viewRef.current = view
    return () => {
      view.destroy()
      viewRef.current = null
    }
    // value 故意不入依赖：输入时由下方 effect 判等同步，避免每次按键重建丢光标。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filename, readOnly])

  useEffect(() => {
    const view = viewRef.current
    if (view && value !== view.state.doc.toString()) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } })
    }
  }, [value])

  // 搜索结果点击跳转（FR-074）：把光标置于目标行并滚动居中。
  // 依赖 value 一并触发，确保「先载入文件内容、后定位」时内容已就绪再定位。
  useEffect(() => {
    const view = viewRef.current
    if (!view || !gotoLine || gotoLine < 1) return
    const lineCount = view.state.doc.lines
    const target = Math.min(gotoLine, lineCount)
    const line = view.state.doc.line(target)
    view.dispatch({
      selection: EditorSelection.cursor(line.from),
      scrollIntoView: true,
    })
    view.focus()
  }, [gotoLine, gotoNonce, value])

  return <div ref={hostRef} className="h-full overflow-hidden text-left" />
}
