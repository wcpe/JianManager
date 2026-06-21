import { useEffect, useRef } from 'react'
import { EditorState, type Extension } from '@codemirror/state'
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

/**
 * 共享 CodeMirror 6 编辑器（FR-070 编辑器基础）。
 *
 * 在 FR-008 编辑器之上：
 * - 多格式高亮（yaml/json/properties/ini/toml/...，见 language.ts）；
 * - Ctrl+S / Cmd+S 拦截 → preventDefault 并回调 onSave（接 FR-051 改前快照/版本）；
 * - 行号 / 撤销重做 / 括号匹配 / 自动缩进 / 折行。
 *
 * FR-073 迷你 IDE 增强会在此基础上叠加搜索替换/批量注释等扩展，故对外暴露稳定 props。
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

  return <div ref={hostRef} className="h-full overflow-hidden text-left" />
}
