/**
 * @file 托管配置的 YAML 编辑器（带注释 + 实时校验）。
 *
 * ## 为什么不是普通 Textarea
 * 上层要「实时校验并把失败行指出来」。纯 Textarea 做不到把光标跳到出错行列，
 * 也没有缩进引导——而 YAML 的失败几乎全是缩进造成的。这里复用项目已有的
 * CodeMirror 6 + `@codemirror/lang-yaml`，行号、缩进、高亮、撤销全都现成。
 *
 * ## 受控语义
 * `value` 是唯一真值（由页面持有）。本组件只负责：渲染、把用户输入向上抛、
 * 解析校验并在本地展示错误位置。**不自己存一份文本**——存两份就会出现
 * 「可视化表单改了值，YAML 里还是旧的」这种谁也解释不清的状态。
 */
import { useEffect, useRef } from 'react'
import { EditorView, lineNumbers, highlightActiveLine, highlightActiveLineGutter, keymap } from '@codemirror/view'
import { EditorState, Compartment } from '@codemirror/state'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { bracketMatching, indentOnInput, syntaxHighlighting, defaultHighlightStyle } from '@codemirror/language'
import { yaml } from '@codemirror/lang-yaml'
import { AlertTriangle, Check, WandSparkles } from 'lucide-react'

import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'

import type { ManagedKeyMeta } from '@/api/managedConfig'
import { stringifyManagedYaml, validateManagedYaml } from '@/lib/managed-config/managed-yaml'

export interface ManagedYamlEditorProps {
  /**
   * 权威目录（可选）。
   *
   * 现在只有「重建注释」这一个动作需要它；注释生成已统一由页面调
   * `stringifyManagedYaml(doc, catalog.items)` 完成，编辑器本身不再自持一份目录，
   * 避免两处各算一次注释、结果不一致。
   */
  items?: ManagedKeyMeta[]
  /** YAML 文本（唯一真值）。 */
  value: string
  /** 文本变化。 */
  onChange: (text: string) => void
  /** 解析结果变化（页面据此拦保存）。 */
  onValidityChange?: (valid: boolean) => void
  /** 只读。 */
  readOnly?: boolean
  /** 用当前解析结果重新生成一份带注释的 YAML。 */
  onRegenerate?: () => void
  /** 高度类名覆盖（折叠区里可以矮一点）。 */
  className?: string
}

/** 明暗主题跟随页面（主题 store 把 `dark` 类写在 <html> 上）。 */
function detectDark(): boolean {
  if (typeof document === 'undefined') return false
  return document.documentElement.classList.contains('dark')
}

export function ManagedYamlEditor({
  value,
  onChange,
  onValidityChange,
  readOnly = false,
  onRegenerate,
  className,
}: ManagedYamlEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  /** 外部灌入的文本，用来避免把自己的输入再当成外部变更回灌。 */
  const externalRef = useRef(value)
  const onChangeRef = useRef(onChange)
  const onValidityRef = useRef(onValidityChange)

  useEffect(() => {
    onChangeRef.current = onChange
    onValidityRef.current = onValidityChange
  }, [onChange, onValidityChange])

  // 解析态直接由当前文本算出，不另存 state：少一处可能与文本不同步的地方。
  const parseResult = validateManagedYaml(value)
  const hasError = parseResult.error !== ''

  useEffect(() => {
    onValidityRef.current?.(!hasError)
  }, [hasError])

  useEffect(() => {
    if (!hostRef.current) return
    externalRef.current = value
    const themeCompartment = new Compartment()
    const state = EditorState.create({
      doc: value,
      extensions: [
        lineNumbers(),
        highlightActiveLine(),
        highlightActiveLineGutter(),
        history(),
        bracketMatching(),
        indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        yaml(),
        keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
        EditorView.lineWrapping,
        themeCompartment.of(detectDark() ? darkTheme : lightTheme),
        EditorView.updateListener.of((update) => {
          if (!update.docChanged) return
          const text = update.state.doc.toString()
          // 外部灌入触发的变化不再回抛，否则会和页面形成回环。
          if (text === externalRef.current) return
          externalRef.current = text
          onChangeRef.current(text)
        }),
        EditorView.editable.of(!readOnly),
      ],
    })
    const view = new EditorView({ state, parent: hostRef.current })
    viewRef.current = view
    // 主题是 class 而非媒体查询，订阅 <html> 的 class 变化同步配色。
    const observer = new MutationObserver(() => {
      view.dispatch({ effects: themeCompartment.reconfigure(detectDark() ? darkTheme : lightTheme) })
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => {
      observer.disconnect()
      view.destroy()
      viewRef.current = null
    }
    // value 只用于初始文档；后续同步交给下面的 effect，否则每次输入都会重建编辑器（丢光标）。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readOnly])

  // 外部文本变化 → 同步进编辑器（可视化表单改了值、或点了重建注释）。
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    const current = view.state.doc.toString()
    if (current === value) return
    externalRef.current = value
    view.dispatch({ changes: { from: 0, to: current.length, insert: value } })
  }, [value])

  const gotoErrorLine = () => {
    const view = viewRef.current
    if (!view || parseResult.line <= 0) return
    const line = view.state.doc.line(Math.min(parseResult.line, view.state.doc.lines))
    view.dispatch({ selection: { anchor: line.from }, scrollIntoView: true })
    view.focus()
  }

  return (
    <div className="flex flex-col gap-2" data-testid="managed-yaml-editor">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="text-xs font-medium">YAML 配置</span>
          <Badge variant="outline" className="text-[10px]">
            带注释 · 实时校验
          </Badge>
        </div>
        <div className="flex items-center gap-2">
          {hasError ? (
            <span className="flex items-center gap-1 text-xs text-destructive" data-testid="managed-yaml-status-error">
              <AlertTriangle aria-hidden className="size-3.5" />
              {/* 行号是 YAML 报错里唯一有用的定位信息，优先展示。 */}
              {parseResult.line > 0 ? `第 ${parseResult.line} 行：` : ''}
              {parseResult.error}
            </span>
          ) : (
            <span className="flex items-center gap-1 text-xs text-status-success" data-testid="managed-yaml-status-ok">
              <Check aria-hidden className="size-3.5" />
              YAML 格式正确
            </span>
          )}
        </div>
      </div>

      <div
        ref={hostRef}
        className={`overflow-hidden rounded-md border bg-background [&_.cm-editor]:text-xs [&_.cm-scroller]:overflow-auto ${
          className ?? '[&_.cm-editor]:max-h-[420px]'
        }`}
        data-testid="managed-yaml-host"
      />

      <div className="flex flex-wrap items-center gap-2">
        {onRegenerate ? (
          <Button type="button" size="xs" variant="outline" onClick={onRegenerate} data-testid="managed-yaml-regenerate">
            <WandSparkles />
            重建注释
          </Button>
        ) : null}
        {hasError && parseResult.line > 0 ? (
          <Button type="button" size="xs" variant="ghost" onClick={gotoErrorLine} data-testid="managed-yaml-goto-error">
            跳到第 {parseResult.line} 行
          </Button>
        ) : null}
        <span className="text-[11px] text-muted-foreground">注释由配置目录自动生成；改动会同步到可视化表单</span>
      </div>
    </div>
  )
}

/** 编辑器深色配色（跟随 shadcn 的 token 色值，不引入新主题包）。 */
const darkTheme = EditorView.theme(
  {
    '&': { color: '#e4e4e7', backgroundColor: 'transparent' },
    '.cm-content': { caretColor: '#e4e4e7' },
    '.cm-gutters': { backgroundColor: 'transparent', color: '#71717a', border: 'none' },
    '.cm-activeLine': { backgroundColor: 'rgba(255,255,255,0.04)' },
    '.cm-activeLineGutter': { backgroundColor: 'rgba(255,255,255,0.04)' },
    '&.cm-focused': { outline: 'none' },
  },
  { dark: true },
)

/** 编辑器浅色配色。 */
const lightTheme = EditorView.theme({
  '&': { color: '#18181b', backgroundColor: 'transparent' },
  '.cm-content': { caretColor: '#18181b' },
  '.cm-gutters': { backgroundColor: 'transparent', color: '#a1a1aa', border: 'none' },
  '.cm-activeLine': { backgroundColor: 'rgba(0,0,0,0.03)' },
  '.cm-activeLineGutter': { backgroundColor: 'rgba(0,0,0,0.03)' },
  '&.cm-focused': { outline: 'none' },
})

/** 供页面重建注释用：把对象重新序列化成带注释的 YAML。 */
export function regenerateManagedYaml(doc: Record<string, unknown>, items: ManagedKeyMeta[]): string {
  return stringifyManagedYaml(doc, items)
}
