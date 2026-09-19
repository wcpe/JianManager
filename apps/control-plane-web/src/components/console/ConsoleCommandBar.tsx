import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ClipboardEvent,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { CornerDownLeft, History, Terminal as TerminalIcon, User } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { cn } from '@jianmanager/ui'

import { searchCommandHistory, type CommandHistoryMatch } from '@/lib/console-command-history'
import {
  applyCommonPrefix,
  applyCompletion,
  computeCompletion,
  type CompletionState,
} from '@/lib/console-completion'

/**
 * 控制台命令栏（FR-415 spec §2.2 + FR-416 智能输入；ADR-086）：原生 `<input>`。
 *
 * 之所以必须是原生控件：Worker 给的是 stdin 管道而不是 pty，旧实现只能在 xterm 的
 * `onData` 里手工重建行编辑器——逐字符回显、手工退格、手工 ↑↓——结果长着终端的样子
 * 却没有终端的能力（← 只会往 stdin 发 `ESC[D`、readline 键全失效、输入法组合过程被打乱）。
 * 换成原生 input，**行编辑 / 输入法 / 粘贴 / 撤销全部免费获得**。
 *
 * 本组件补浏览器不给、而排障确实需要的几样（FR-416）：
 * - readline 惯用键（`Ctrl+A/E/W/U`）与 ↑↓ 历史（FR-415）
 * - `Ctrl+R` 模糊搜索历史（历史的持久化由宿主负责，此处只消费 `history`）
 * - 候选式 Tab 补全：**多候选时只推进到公共前缀 + 开候选列表，绝不盲补第一个**
 * - 多行粘贴保护：≥2 行先问「逐行发送 / 仅首行 / 取消」，不一股脑连发
 */

export interface ConsoleCommandBarProps {
  /** 非 RUNNING 即禁用（spec §2.2）。 */
  disabled?: boolean
  /** 禁用原因（一行说明，与禁用同时出现，不让用户猜为什么输不进去）。 */
  disabledReason?: string
  /** 直达动作（停机/崩溃态的「启动实例」按钮等）。 */
  action?: ReactNode
  /** 提交整行。**不含行尾**：Worker 侧写 stdin 时自行补换行。 */
  onSubmit: (line: string) => void
  /** 命令历史（旧→新）。↑ 从最新一条开始回溯；`^R` 在其上模糊搜索。 */
  history?: readonly string[]
  /**
   * 在线玩家名（FR-416 第二段补全的候选）。
   *
   * **必须来自真实来源**（`/players` 在线聚合 + 控制台 join/quit/list 行），
   * 本组件不内置任何玩家名——补出一个不存在的玩家只会让命令白跑一趟。
   */
  players?: readonly string[]
  fontSize?: number
  /** 沉浸工作台使用固定深色表面，避免亮色主题把命令栏渲成白底白字。 */
  immersive?: boolean
}

/** 从光标向前删一个词（先吃空白、再吃非空白），返回新值与新光标位。 */
function deleteWordBefore(value: string, caret: number): { value: string; caret: number } {
  let at = caret
  while (at > 0 && /\s/.test(value[at - 1])) at--
  while (at > 0 && !/\s/.test(value[at - 1])) at--
  return { value: value.slice(0, at) + value.slice(caret), caret: at }
}

/**
 * 把粘贴文本切成待发送行。
 *
 * 尾部空行丢弃：从别处复制的一条命令常带尾随换行（`stop\n`），那仍是**一行**，
 * 不该弹确认；中间的空行同样丢弃（往 MC 送空命令只会刷 Unknown command）。
 */
function splitPastedLines(text: string): string[] {
  return text
    .split(/\r\n|\r|\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
}

const COMPLETION_ICON: Record<CompletionState['kind'], typeof TerminalIcon> = {
  command: TerminalIcon,
  player: User,
  selector: User,
}

export default function ConsoleCommandBar({
  disabled = false,
  disabledReason,
  action,
  onSubmit,
  history = [],
  players = [],
  fontSize = 14,
  immersive = false,
}: ConsoleCommandBarProps) {
  const { t } = useTranslation()
  const inputRef = useRef<HTMLInputElement>(null)
  const [value, setValue] = useState('')
  /** 历史游标：-1 = 正在编辑草稿，≥0 = 指向 history 的下标。 */
  const [historyIndex, setHistoryIndex] = useState(-1)
  const draftRef = useRef('')
  /** 需要在 value 落定后设置的光标位（React 受控 input 无法在 setState 同步设选区）。 */
  const pendingCaretRef = useRef<number | null>(null)

  // ---- FR-416：Tab 补全候选 ----
  const [completion, setCompletion] = useState<{ state: CompletionState; index: number } | null>(null)
  // ---- FR-416：^R 反向历史搜索 ----
  const [reverseSearch, setReverseSearch] = useState<{ query: string; index: number } | null>(null)
  const reverseInputRef = useRef<HTMLInputElement>(null)
  // ---- FR-416：多行粘贴确认 ----
  const [pastedLines, setPastedLines] = useState<string[] | null>(null)

  useEffect(() => {
    const caret = pendingCaretRef.current
    if (caret == null) return
    pendingCaretRef.current = null
    inputRef.current?.setSelectionRange(caret, caret)
  }, [value])

  const reverseSearchOpen = reverseSearch !== null
  useLayoutEffect(() => {
    if (reverseSearchOpen) reverseInputRef.current?.focus()
  }, [reverseSearchOpen])

  const applyEdit = useCallback((next: string, caret: number) => {
    setValue(next)
    pendingCaretRef.current = caret
  }, [])

  const submitLine = useCallback(
    (line: string) => {
      // 空行不下发：往 MC 控制台送空命令只会刷一条无意义的 Unknown command。
      if (!line.trim()) return
      onSubmit(line)
    },
    [onSubmit],
  )

  const submit = useCallback(() => {
    if (!value.trim()) return
    submitLine(value)
    setValue('')
    setHistoryIndex(-1)
    setCompletion(null)
    draftRef.current = ''
  }, [submitLine, value])

  const navigateHistory = useCallback(
    (delta: -1 | 1) => {
      if (history.length === 0) return
      if (historyIndex === -1) {
        if (delta === 1) return // 已在草稿，↓ 无处可去
        draftRef.current = value
        const index = history.length - 1
        setHistoryIndex(index)
        applyEdit(history[index], history[index].length)
        return
      }
      const next = historyIndex + delta
      if (next < 0) return // 已到最早一条，继续 ↑ 不动
      if (next >= history.length) {
        setHistoryIndex(-1)
        applyEdit(draftRef.current, draftRef.current.length)
        return
      }
      setHistoryIndex(next)
      applyEdit(history[next], history[next].length)
    },
    [applyEdit, history, historyIndex, value],
  )

  const acceptCompletion = (candidate: string) => {
    if (!completion) return
    const next = applyCompletion(value, completion.state, candidate)
    applyEdit(next.value, next.caret)
    setCompletion(null)
    inputRef.current?.focus()
  }

  const handleTab = (event: KeyboardEvent<HTMLInputElement>) => {
    const input = event.currentTarget
    // 已开候选：Tab 在候选间下移（显式动作，不是盲补）。
    if (completion) {
      event.preventDefault()
      setCompletion({ ...completion, index: (completion.index + 1) % completion.state.candidates.length })
      return
    }
    const caret = input.selectionStart ?? input.value.length
    const state = computeCompletion(input.value, caret, players)
    // 无候选就把 Tab 交还浏览器（焦点移走），不吞掉无障碍键。
    if (!state) return
    event.preventDefault()

    if (state.candidates.length === 1) {
      // 唯一候选直接补：这不是「盲补」，是无歧义。
      const next = applyCompletion(input.value, state, state.candidates[0])
      applyEdit(next.value, next.caret)
      setCompletion(null)
      return
    }
    // 多候选：只推进到**所有候选都认的公共前缀**，其余交给用户选（FR-416「不盲补」）。
    const advanced = applyCommonPrefix(input.value, state)
    if (advanced) {
      applyEdit(advanced.value, advanced.caret)
      const refreshed = computeCompletion(advanced.value, advanced.caret, players)
      setCompletion(refreshed ? { state: refreshed, index: 0 } : null)
      return
    }
    setCompletion({ state, index: 0 })
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    // 输入法组合期间一律不拦截：组合中的 Enter 是「确认候选词」，抢走就会把拼音
    // 当命令提交、并让候选框里的字重复回显——正是 ADR-086 要消灭的那类病。
    if (event.nativeEvent.isComposing) return

    const input = event.currentTarget
    const caret = input.selectionStart ?? input.value.length

    if (event.key === 'Tab' && !event.ctrlKey && !event.altKey && !event.metaKey) {
      handleTab(event)
      return
    }

    // ^R 打开历史模糊搜索。Ctrl+R 是浏览器刷新键，但（不同于 Ctrl+W/T/N）可被 preventDefault
    // 拦下；命令栏聚焦时把它借给 readline 语义，符合用户在终端里的肌肉记忆。
    if (event.ctrlKey && !event.altKey && !event.metaKey && event.key.toLowerCase() === 'r') {
      event.preventDefault()
      setCompletion(null)
      setReverseSearch({ query: '', index: 0 })
      return
    }

    if (completion) {
      const { candidates } = completion.state
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setCompletion({ ...completion, index: (completion.index + 1) % candidates.length })
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setCompletion({ ...completion, index: (completion.index - 1 + candidates.length) % candidates.length })
        return
      }
      if (event.key === 'Enter') {
        // 候选列表开着时 Enter 是「确认候选」而非「提交命令」：否则用户刚打开候选就
        // 误提交一条半截命令。要提交先 Esc 关列表。
        event.preventDefault()
        acceptCompletion(candidates[completion.index])
        return
      }
      if (event.key === 'Escape') {
        event.preventDefault()
        setCompletion(null)
        return
      }
    }

    if (event.key === 'Enter') {
      event.preventDefault()
      submit()
      return
    }
    if (event.key === 'ArrowUp' && !event.altKey && !event.ctrlKey && !event.metaKey) {
      event.preventDefault()
      navigateHistory(-1)
      return
    }
    if (event.key === 'ArrowDown' && !event.altKey && !event.ctrlKey && !event.metaKey) {
      event.preventDefault()
      navigateHistory(1)
      return
    }

    // readline 惯用键：ADR-086 点名这几个键「全部失效」是弃 xterm 输入的直接动因之一。
    // Ctrl+A/E 覆盖浏览器的「全选/无动作」，取 readline 的行首/行尾语义。
    if (event.ctrlKey && !event.altKey && !event.metaKey) {
      const key = event.key.toLowerCase()
      if (key === 'a') {
        event.preventDefault()
        input.setSelectionRange(0, 0)
        return
      }
      if (key === 'e') {
        event.preventDefault()
        input.setSelectionRange(input.value.length, input.value.length)
        return
      }
      if (key === 'u') {
        event.preventDefault()
        applyEdit(input.value.slice(caret), 0)
        return
      }
      if (key === 'w' || event.key === 'Backspace') {
        // Ctrl+W 在 Chrome/Edge 是保留的「关闭标签页」快捷键，preventDefault 未必拦得住；
        // 故同时把 Ctrl+Backspace（浏览器原生删词）与 Alt+Backspace 走同一路径，
        // 保证「删一个词」这件事总有可用按键。
        event.preventDefault()
        const next = deleteWordBefore(input.value, caret)
        applyEdit(next.value, next.caret)
        return
      }
    }
    if (event.altKey && event.key === 'Backspace') {
      event.preventDefault()
      const next = deleteWordBefore(input.value, caret)
      applyEdit(next.value, next.caret)
    }
  }

  /**
   * 多行粘贴保护（FR-416）。
   *
   * 单行粘贴一律放行给浏览器原生路径——`readText()` 在 HTTP 非安全上下文不可用（FIX-1），
   * 原生 paste 事件则始终可用，不能为了「统一处理」把它接管掉。
   * 只有 ≥2 行才拦下问用户：粘一段 10 行的脚本进控制台，一股脑连发过去往往不是本意。
   */
  const handlePaste = (event: ClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData?.getData('text') ?? ''
    const lines = splitPastedLines(text)
    if (lines.length < 2) return
    event.preventDefault()
    setCompletion(null)
    setPastedLines(lines)
  }

  const sendPastedLines = () => {
    const lines = pastedLines ?? []
    setPastedLines(null)
    for (const line of lines) submitLine(line)
    setValue('')
    setHistoryIndex(-1)
    draftRef.current = ''
  }

  const takeFirstPastedLine = () => {
    const first = pastedLines?.[0] ?? ''
    setPastedLines(null)
    const input = inputRef.current
    const caret = input?.selectionStart ?? value.length
    const next = `${value.slice(0, caret)}${first}${value.slice(caret)}`
    applyEdit(next, caret + first.length)
    input?.focus()
  }

  // ---- ^R 浮层 ----
  const reverseMatches: CommandHistoryMatch[] = reverseSearch
    ? searchCommandHistory(history, reverseSearch.query)
    : []
  const reverseIndex = reverseMatches.length > 0 ? Math.min(reverseSearch?.index ?? 0, reverseMatches.length - 1) : 0

  const closeReverseSearch = () => {
    setReverseSearch(null)
    inputRef.current?.focus()
  }

  const fillFromReverseSearch = (line: string) => {
    setReverseSearch(null)
    // 填入而非直接提交：`^R` 找到的命令常需要改个参数再发，直接发出去很危险。
    applyEdit(line, line.length)
    setHistoryIndex(-1)
    inputRef.current?.focus()
  }

  const handleReverseKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.nativeEvent.isComposing) return
    if (event.key === 'Escape') {
      event.preventDefault()
      closeReverseSearch()
      return
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (reverseMatches.length > 0) {
        setReverseSearch({ query: reverseSearch?.query ?? '', index: (reverseIndex + 1) % reverseMatches.length })
      }
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      if (reverseMatches.length > 0) {
        setReverseSearch({
          query: reverseSearch?.query ?? '',
          index: (reverseIndex - 1 + reverseMatches.length) % reverseMatches.length,
        })
      }
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      const hit = reverseMatches[reverseIndex]
      if (hit) fillFromReverseSearch(hit.value)
      else closeReverseSearch()
    }
  }

  /**
   * Ghost 预览（FR-416）：把高亮候选的剩余部分以灰字排在光标之后。
   *
   * 两个前提缺一不可：
   * ① token 就在行尾（否则 ghost 会盖住光标之后的既有文本，看起来像输入被篡改）；
   * ② 候选**大小写一致地**以 token 开头——用户敲 `st` 而候选是 `Steve` 时，
   *    显示 `st` + `eve` 会看成 `steve`，那是个服务端不认的名字，属视觉谎言。
   *    这种情况不画 ghost，候选列表已经把真实候选摆在那里了。
   */
  const activeCandidate = completion?.state.candidates[completion.index]
  const ghost =
    completion &&
    activeCandidate &&
    completion.state.tokenEnd === value.length &&
    activeCandidate.startsWith(completion.state.token)
      ? activeCandidate.slice(completion.state.token.length)
      : ''

  return (
    <div className={cn('relative shrink-0 border-t px-2 py-1.5', immersive ? 'border-white/10 bg-[#171b22] px-3 py-2' : 'bg-card/40')}>
      {disabled && disabledReason && (
        <div className={cn('mb-1.5 flex items-center gap-2 text-xs', immersive ? 'text-amber-300' : 'text-amber-600 dark:text-amber-400')}>
          <span>{disabledReason}</span>
          {action}
        </div>
      )}

      {/* ^R 历史搜索浮层：浮在命令栏上方，不顶开布局（ui-modals 纪律：不内联展开挤占页面）。 */}
      {reverseSearch && (
        <div
          role="dialog"
          aria-label={t('instanceDetail.consoleHistorySearchTitle')}
          className={cn('absolute bottom-full left-2 right-2 z-30 mb-1 rounded-md border shadow-lg', immersive ? 'border-white/10 bg-[#1b2029] text-slate-100' : 'bg-popover')}
        >
          <div className="flex items-center gap-2 border-b px-2 py-1.5">
            <History className="size-3.5 shrink-0 text-muted-foreground" />
            <input
              ref={reverseInputRef}
              type="text"
              value={reverseSearch.query}
              onChange={(event) => setReverseSearch({ query: event.target.value, index: 0 })}
              onKeyDown={handleReverseKeyDown}
              aria-label={t('instanceDetail.consoleHistorySearchInput')}
              placeholder={t('instanceDetail.consoleHistorySearchPlaceholder')}
              autoComplete="off"
              spellCheck={false}
              className="h-7 min-w-0 flex-1 bg-transparent font-mono text-xs outline-none"
            />
            <span className="shrink-0 text-[11px] text-muted-foreground">
              {t('instanceDetail.consoleHistorySearchCount', { count: reverseMatches.length })}
            </span>
          </div>
          <ul role="listbox" aria-label={t('instanceDetail.consoleHistorySearchResults')} className="max-h-56 overflow-y-auto py-1">
            {reverseMatches.length === 0 ? (
              <li className="px-3 py-1.5 text-xs text-muted-foreground">{t('instanceDetail.consoleHistorySearchEmpty')}</li>
            ) : (
              reverseMatches.map((match, index) => (
                <li key={match.value}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={index === reverseIndex}
                    onClick={() => fillFromReverseSearch(match.value)}
                    className={cn(
                      'block w-full truncate px-3 py-1 text-left font-mono text-xs',
                      index === reverseIndex ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/50',
                    )}
                  >
                    {match.value}
                  </button>
                </li>
              ))
            )}
          </ul>
          <div className="border-t px-3 py-1 text-[11px] text-muted-foreground">
            {t('instanceDetail.consoleHistorySearchHint')}
          </div>
        </div>
      )}

      {/* 补全候选浮层：同样浮在上方，且不盲补——列表只是高亮，未按 Enter/Tab 前输入不变。 */}
      {completion && !reverseSearch && (
        <div
          className={cn('absolute bottom-full left-6 z-20 mb-1 min-w-40 max-w-[min(28rem,calc(100%-3rem))] rounded-md border shadow-lg', immersive ? 'border-white/10 bg-[#1b2029] text-slate-100' : 'bg-popover')}
        >
          <ul
            role="listbox"
            aria-label={t('instanceDetail.consoleCompletionLabel')}
            className="max-h-56 overflow-y-auto py-1"
          >
            {completion.state.candidates.map((candidate, index) => {
              const Icon = COMPLETION_ICON[completion.state.kind]
              return (
                <li key={candidate}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={index === completion.index}
                    // mousedown 阻默认：否则输入框先失焦，onBlur 关掉候选列表，
                    // click 落到一个已经不存在的项上——「点候选没反应」的经典成因。
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={() => acceptCompletion(candidate)}
                    className={cn(
                      'flex w-full items-center gap-1.5 px-2.5 py-1 text-left font-mono text-xs',
                      index === completion.index ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/50',
                    )}
                  >
                    <Icon className="size-3 shrink-0 opacity-60" />
                    <span className="truncate">{candidate}</span>
                  </button>
                </li>
              )
            })}
          </ul>
          <div className="border-t px-2.5 py-1 text-[11px] text-muted-foreground">
            {t('instanceDetail.consoleCompletionHint')}
          </div>
        </div>
      )}

      <div className="flex items-center gap-2">
        {/* 沉浸态的 `>` prompt：给输入框一个终端式的锚点；普通控制台无此装饰。 */}
        {immersive && <span aria-hidden className="shrink-0 font-mono text-sm text-primary/80">{'>'}</span>}
        <div className="relative flex min-w-0 flex-1 items-center">
          <input
            ref={inputRef}
            type="text"
            value={value}
            disabled={disabled}
            data-testid="console-command-input"
            data-terminal-access={disabled ? 'denied' : 'allowed'}
            onChange={(event) => {
              setValue(event.target.value)
              // 候选开着时随输入重算：不重算会让列表停留在过时的前缀上。
              if (completion) {
                const state = computeCompletion(event.target.value, event.target.selectionStart ?? event.target.value.length, players)
                setCompletion(state ? { state, index: 0 } : null)
              }
            }}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
            onBlur={() => setCompletion(null)}
            aria-label={t('instanceDetail.consoleCommandLabel')}
            placeholder={
              disabled ? t('instanceDetail.consoleCommandDisabledPlaceholder') : t('instanceDetail.consoleCommandPlaceholder')
            }
            // autoComplete/spellCheck 关掉：浏览器的表单补全与拼写红线对游戏服命令毫无意义。
            autoComplete="off"
            autoCorrect="off"
            spellCheck={false}
            style={{ fontSize }}
            className={cn(
              'h-8 w-full min-w-0 rounded-md border px-2 font-mono outline-none',
              immersive
                ? 'border-white/10 bg-[#101419] text-slate-100 placeholder:text-slate-500 focus:border-primary focus:ring-1 focus:ring-primary/40'
                : 'bg-background focus:border-primary',
              'disabled:cursor-not-allowed disabled:opacity-60',
            )}
          />
          {/* Ghost 预览：等宽字体下用不可见的已输入文本占位，把候选剩余部分排在光标后。 */}
          {ghost && (
            <span
              aria-hidden
              data-testid="console-completion-ghost"
              style={{ fontSize }}
              className="pointer-events-none absolute inset-y-0 left-0 flex items-center border border-transparent px-2 font-mono"
            >
              <span className="invisible whitespace-pre">{value}</span>
              <span className="whitespace-pre text-muted-foreground/50">{ghost}</span>
            </span>
          )}
        </div>
        <Button size="sm" variant={immersive ? 'default' : 'secondary'} className={cn('h-8 shrink-0 px-2', immersive && 'bg-primary/90 text-primary-foreground hover:bg-primary')} disabled={disabled || !value.trim()} onClick={submit}>
          <CornerDownLeft className="mr-1 size-3.5" />
          {t('instanceDetail.consoleCommandSend')}
        </Button>
      </div>

      {/* 多行粘贴确认（FR-416）：走模态而非内联展开（.claude/rules/ui-modals.md）。 */}
      <Dialog open={pastedLines !== null} onOpenChange={(open) => !open && setPastedLines(null)}>
        <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-lg')}>
          <DialogHeader>
            <DialogTitle>{t('instanceDetail.consolePasteMultilineTitle')}</DialogTitle>
            <DialogDescription>
              {t('instanceDetail.consolePasteMultilineDesc', { count: pastedLines?.length ?? 0 })}
            </DialogDescription>
          </DialogHeader>
          <ScrollableDialogBody>
            <ol className="space-y-0.5 rounded-md border bg-muted/40 p-2 font-mono text-xs">
              {(pastedLines ?? []).map((line, index) => (
                <li key={`${index}-${line}`} className="flex gap-2">
                  <span className="w-6 shrink-0 text-right text-muted-foreground tabular-nums">{index + 1}</span>
                  <span className="min-w-0 break-all">{line}</span>
                </li>
              ))}
            </ol>
          </ScrollableDialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setPastedLines(null)}>
              {t('common.cancel')}
            </Button>
            <Button variant="outline" onClick={takeFirstPastedLine}>
              {t('instanceDetail.consolePasteFirstOnly')}
            </Button>
            <Button onClick={sendPastedLines}>
              {t('instanceDetail.consolePasteSendAll', { count: pastedLines?.length ?? 0 })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
