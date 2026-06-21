import { useEffect, useRef } from 'react'
import { EditorState } from '@codemirror/state'
import {
  EditorView,
  lineNumbers,
  keymap,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import {
  syntaxHighlighting,
  defaultHighlightStyle,
  indentOnInput,
  bracketMatching,
} from '@codemirror/language'
import { json } from '@codemirror/lang-json'
import { yaml } from '@codemirror/lang-yaml'

/** 按文件名后缀选择 CodeMirror 语言扩展（FR-008：YAML/JSON 语法高亮，其余纯文本）。 */
function languageFor(filename: string) {
  if (/\.ya?ml$/i.test(filename)) return [yaml()]
  if (/\.json$/i.test(filename)) return [json()]
  return []
}

/** CodeMirror 文件编辑器（FR-008 在线编辑/查看）。 */
interface CodeEditorProps {
  /** 文档内容 */
  value: string
  /** 文件名，决定语法高亮语言 */
  filename: string
  /** 只读（查看态） */
  readOnly?: boolean
  /** 编辑回调 */
  onChange?: (value: string) => void
}

/**
 * 轻量 CodeMirror 6 编辑器：YAML/JSON 语法高亮 + 行号 + 撤销/重做（FR-008）。
 * 编辑器实例随 filename/readOnly 变化重建；外部 value 变化（载入新文件）经第二个 effect 同步，不打断输入。
 */
export default function CodeEditor({ value, filename, readOnly = false, onChange }: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const onChangeRef = useRef(onChange)
  // 在 effect 中同步最新 onChange（避免渲染期写 ref）；updateListener 经 ref 取最新回调。
  useEffect(() => {
    onChangeRef.current = onChange
  }, [onChange])

  useEffect(() => {
    if (!hostRef.current) return
    const view = new EditorView({
      parent: hostRef.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          history(),
          drawSelection(),
          indentOnInput(),
          bracketMatching(),
          highlightActiveLine(),
          highlightActiveLineGutter(),
          syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
          keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
          ...languageFor(filename),
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
        ],
      }),
    })
    viewRef.current = view
    return () => {
      view.destroy()
      viewRef.current = null
    }
    // value 故意不入依赖：输入时由下方 effect 判等同步，避免每次按键重建丢光标
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
