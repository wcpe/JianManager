import * as React from 'react'
import { SearchIcon } from 'lucide-react'
import { Dialog as DialogPrimitive } from 'radix-ui'

import { cn } from '../../lib/utils'

/**
 * 命令面板（FR-496 阶段 4）：⌘K / Ctrl+K 与顶栏九宫格共用的统一检索入口。
 *
 * 为什么要有它：本轮把常驻侧栏从「所有页面路由」收敛为「资源优先」，被移出侧栏的页面
 * 不能因此变得难找。命令面板与「全部功能」目录是检索捷径——**不是**低频功能唯一的入口，
 * 用户仍应能从明确分类里找到功能（`.tmp/设计/布局设计与交付说明.md` 第四节）。
 *
 * 设计要点：
 * ① **受控开合**。面板不自己持有 open 状态：⌘K 是在全局外壳上监听的，由外壳决定开合；
 *    组件只把 Radix 的 onOpenChange 原样上抛，避免出现两处真相。
 * ② **检索口径覆盖「原路径」**。原型要求「Ctrl/⌘+K 可以用实例名、节点名、原页面名、原路由查找」，
 *    所以匹配词不只看 label——`keywords` 承担原页面名与原路由，`hint`、`group` 也一并参与，
 *    这样迁移路由时只要把旧路径塞进 keywords，用户记着的旧地址就还能找到入口。
 * ③ **基于 radix-ui 的 Dialog 原语，而不是共享的 `<DialogContent>`**。共享 Dialog 是「表单型弹窗」：
 *    它刻意拦截外部点击不关闭、固定在视口正中、自带右上角关闭按钮。命令面板三条都要反过来
 *    （点遮罩要关、贴近顶部、无关闭按钮），因此这里直接用同一套 radix-ui 原语拼装，
 *    复用它的焦点陷阱与 Esc 语义，但把定位与关闭行为按检索型弹窗重写。
 */

/** 一条可检索的命令结果。 */
export interface CommandPaletteItem {
  /** 稳定唯一 ID：同时用于 React key 与 aria-activedescendant 的落点。 */
  id: string
  /** 主文案，参与匹配与高亮（实例名 / 节点名 / 原页面名）。 */
  label: string
  /** 分组标题首项（如「页面」「实例」「节点」）；items 的顺序决定分组顺序。 */
  group: string
  /** 右侧提示，通常放原路由（`/instances`）；也参与匹配。 */
  hint?: string
  /**
   * 补充匹配词（支持按原页面名、原路由查找）。
   * 例：`['实例列表', '/instances', '全部实例']`。
   */
  keywords?: string[]
  /** 前置图标（lucide 组件），可选。 */
  icon?: React.ReactNode
  /** 选中后的动作，通常是导航。 */
  onSelect: () => void
}

/** 把查询串切成小写关键词；全部命中才算匹配（多词是「与」关系，便于逐步收窄）。 */
function tokenize(query: string): string[] {
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean)
}

/** 一条结果的全部可检索文本。 */
function toHaystack(item: CommandPaletteItem): string {
  return [item.label, item.group, item.hint ?? '', ...(item.keywords ?? [])].join(' ').toLowerCase()
}

/**
 * 把一段文本按命中关键词切成「命中 / 未命中」两种片段，供高亮渲染。
 *
 * 返回分段而非 HTML 字符串：避免 `dangerouslySetInnerHTML`，索引词条是可被后端或用户
 * 任意构造的文本，走字符串拼装就等于把转义责任交给调用方。
 */
function splitHighlight(text: string, tokens: string[]): Array<{ text: string; hit: boolean }> {
  const lower = text.toLowerCase()
  // 先标记每个字符是否落在命中区间内（多个关键词重叠时取并集），再压缩成连续片段
  const marked = new Array<boolean>(text.length).fill(false)
  for (const token of tokens) {
    let from = 0
    for (;;) {
      const at = lower.indexOf(token, from)
      if (at < 0) break
      for (let i = at; i < at + token.length && i < marked.length; i += 1) marked[i] = true
      from = at + token.length
    }
  }

  const parts: Array<{ text: string; hit: boolean }> = []
  for (let i = 0; i < text.length; i += 1) {
    // i 恒在 [0, text.length-1]，而 marked 与 text 等长，两者必存在；判空仅为类型收窄
    const char = text[i]
    const hit = marked[i]
    if (char === undefined || hit === undefined) continue
    const last = parts[parts.length - 1]
    if (last && last.hit === hit) last.text += char
    else parts.push({ text: char, hit })
  }
  return parts
}

/** 高亮片段渲染：命中部分标主色加粗，其余原样。 */
function Highlight({ text, tokens }: { text: string; tokens: string[] }) {
  return (
    <>
      {splitHighlight(text, tokens).map((part, index) =>
        part.hit ? (
          <mark key={index} className="bg-transparent font-semibold text-primary">
            {part.text}
          </mark>
        ) : (
          <React.Fragment key={index}>{part.text}</React.Fragment>
        ),
      )}
    </>
  )
}

/** 按 group 切分已过滤结果，保留 items 的原始顺序（分组顺序即首现顺序）。 */
function groupFiltered(filtered: CommandPaletteItem[]) {
  const groups: Array<{ name: string; entries: Array<{ item: CommandPaletteItem; index: number }> }> =
    []
  filtered.forEach((item, index) => {
    const last = groups[groups.length - 1]
    if (last && last.name === item.group) last.entries.push({ item, index })
    else groups.push({ name: item.group, entries: [{ item, index }] })
  })
  return groups
}

/**
 * 面板主体（输入 + 结果 + 底部提示）。
 *
 * 独立成组件是刻意的：查询词与高亮游标都是「一次打开会话」的临时状态。Radix 在关闭时会卸载
 * Content，于是它们随卸载自然归零，下一次打开必定是干净的空查询与首个结果高亮——
 * 不需要额外的「打开时重置」副作用，也不会出现上次的搜索词残留。
 */
function CommandPaletteBody({
  items,
  inputRef,
  placeholder,
  label,
  emptyLabel,
  emptyHint,
  onDismiss,
}: {
  items: CommandPaletteItem[]
  inputRef: React.RefObject<HTMLInputElement | null>
  placeholder: string
  label: string
  emptyLabel: string
  emptyHint: string
  onDismiss: () => void
}) {
  // 单状态对象：查询词变化时必须同时把游标归零，合成一次更新可避免中间态渲染
  const [state, setState] = React.useState({ query: '', activeIndex: 0 })
  const tokens = React.useMemo(() => tokenize(state.query), [state.query])

  const filtered = React.useMemo(
    () => items.filter((item) => tokens.every((token) => toHaystack(item).includes(token))),
    [items, tokens],
  )
  // 结果集变小后游标可能越界（例如继续输入把选中项过滤掉了），取值时夹紧而不是再写一次副作用
  const activeIndex = Math.min(state.activeIndex, Math.max(0, filtered.length - 1))
  const activeId = filtered[activeIndex]?.id

  const listId = React.useId()
  const itemRefs = React.useRef(new Map<string, HTMLButtonElement>())

  // 键盘移动后把高亮项滚进视野；jsdom 没有 scrollIntoView，故用可选调用兜底
  React.useEffect(() => {
    if (activeId) itemRefs.current.get(activeId)?.scrollIntoView?.({ block: 'nearest' })
  }, [activeId])

  function select(item: CommandPaletteItem) {
    // 先关面板再执行动作：onSelect 往往立刻跳路由，若反过来，面板会先在新页面上闪一下
    onDismiss()
    item.onSelect()
  }

  function handleKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      if (filtered.length === 0) return
      // 到边界即停（不环绕），与原型 `Math.min/max` 的钳制一致，避免误按方向键跳回另一端
      const step = event.key === 'ArrowDown' ? 1 : -1
      setState((prev) => ({
        ...prev,
        activeIndex: Math.max(0, Math.min(filtered.length - 1, activeIndex + step)),
      }))
      return
    }
    if (event.key === 'Enter') {
      const active = filtered[activeIndex]
      if (!active) return
      event.preventDefault()
      select(active)
      return
    }
    if (event.key === 'Escape') {
      // Radix 自身也在 document 上监听 Esc，这里再显式处理一次是为了让「输入框内按 Esc」
      // 的行为不依赖图层实现细节；重复调用 onOpenChange(false) 是幂等的。
      event.preventDefault()
      onDismiss()
    }
  }

  return (
    <>
      {/* 输入区：原型 .modal-search —— 无边框输入贴着图标，视觉上是一整块搜索域 */}
      <div className="flex items-center gap-[10px] border-b px-[17px] py-[15px] text-muted-foreground">
        <SearchIcon className="size-4 shrink-0" aria-hidden />
        <input
          ref={inputRef}
          data-slot="command-palette-input"
          role="combobox"
          aria-label={label}
          aria-expanded
          aria-controls={listId}
          aria-activedescendant={activeId ? `${listId}-${activeId}` : undefined}
          aria-autocomplete="list"
          autoComplete="off"
          placeholder={placeholder}
          value={state.query}
          onChange={(event) => setState({ query: event.target.value, activeIndex: 0 })}
          onKeyDown={handleKeyDown}
          className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
        />
      </div>

      <div
        data-slot="command-palette-list"
        id={listId}
        role="listbox"
        aria-label={label}
        className="max-h-[55vh] overflow-auto p-2"
      >
        {filtered.length === 0 && (
          <div data-slot="command-palette-empty" className="px-3 py-8 text-center">
            <p className="text-xs font-medium text-foreground">{emptyLabel}</p>
            <p className="mt-1 text-[11px] text-muted-foreground">{emptyHint}</p>
          </div>
        )}

        {groupFiltered(filtered).map((group) => (
          <div key={group.name} role="group" aria-label={group.name}>
            <div
              data-slot="command-palette-group"
              className="px-[11px] pb-1 pt-2 text-[10px] text-muted-foreground"
            >
              {group.name}
            </div>
            {group.entries.map(({ item, index }) => (
              <button
                key={item.id}
                type="button"
                data-slot="command-palette-item"
                id={`${listId}-${item.id}`}
                ref={(el) => {
                  if (el) itemRefs.current.set(item.id, el)
                  else itemRefs.current.delete(item.id)
                }}
                role="option"
                aria-selected={index === activeIndex}
                // 悬停即接管高亮游标，否则鼠标停下后方向键会从「看不见的旧位置」继续走
                onMouseEnter={() => setState((prev) => ({ ...prev, activeIndex: index }))}
                onClick={() => select(item)}
                className={cn(
                  'flex w-full items-center gap-[11px] rounded-md p-2.5 text-left',
                  'transition-colors duration-[var(--motion-duration-normal)] ease-ios',
                  index === activeIndex && 'bg-accent text-accent-foreground',
                )}
              >
                {item.icon && (
                  <span className="shrink-0 text-muted-foreground [&_svg]:size-4" aria-hidden>
                    {item.icon}
                  </span>
                )}
                <span className="min-w-0 flex-1 truncate text-xs text-foreground">
                  <Highlight text={item.label} tokens={tokens} />
                </span>
                {item.hint && (
                  <span className="ml-auto shrink-0 font-mono text-[10px] text-muted-foreground">
                    <Highlight text={item.hint} tokens={tokens} />
                  </span>
                )}
              </button>
            ))}
          </div>
        ))}
      </div>

      <div
        data-slot="command-palette-footer"
        className="flex items-center justify-between gap-3 border-t px-[17px] py-2.5 text-[11px] text-muted-foreground"
      >
        <span>↑ ↓ 选择 · Enter 打开</span>
        <span>Esc 关闭</span>
      </div>
    </>
  )
}

/**
 * 命令面板。
 *
 * 受控组件：`open` 由外壳持有，`onOpenChange` 在 Esc、点遮罩、执行结果三条路径上被调用。
 * 快捷键监听（⌘K / Ctrl+K）刻意不放在这里——它属于外壳职责，本组件只保证自己能被开关。
 */
export function CommandPalette({
  open,
  onOpenChange,
  items,
  placeholder = '搜索实例、节点、功能或原路由…',
  label = '命令面板',
  emptyLabel = '没有匹配项',
  emptyHint = '可用实例名、节点名、原页面名或原路由查找。',
  className,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  items: CommandPaletteItem[]
  placeholder?: string
  /** 面板的可访问名（同时作为输入框的 aria-label）。 */
  label?: string
  /** 空结果标题。 */
  emptyLabel?: string
  /** 空结果说明，指引可用的查找口径。 */
  emptyHint?: string
  className?: string
}) {
  const inputRef = React.useRef<HTMLInputElement>(null)

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        {/* 遮罩：与共享 Dialog 同一套 z 轴与淡入淡出 token，但允许外部点击关闭（检索型弹窗的常规预期） */}
        <DialogPrimitive.Overlay
          data-slot="command-palette-overlay"
          className="fixed inset-0 z-[200] bg-black/50 duration-[var(--motion-duration-normal)] data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0"
        />
        <DialogPrimitive.Content
          data-slot="command-palette"
          // 面板没有说明文本：显式声明无描述，避免 Radix 因缺 Description 打可访问性告警
          aria-describedby={undefined}
          // 打开即把焦点交给检索框。Radix 默认聚焦内容容器本身，那样用户还得先按一次 Tab，
          // 与 ⌘K「呼出即可打字」的预期不符。
          onOpenAutoFocus={(event) => {
            event.preventDefault()
            inputRef.current?.focus()
          }}
          className={cn(
            'fixed left-1/2 top-[12vh] z-[200] w-[min(560px,calc(100%-2rem))] -translate-x-1/2 overflow-hidden',
            'rounded-lg border bg-card shadow-lg outline-none',
            'duration-[var(--motion-duration-normal)] data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95',
            className,
          )}
        >
          {/* 视觉上无标题（检索框即标题），但 Radix 需要一个可访问名 */}
          <DialogPrimitive.Title className="sr-only">{label}</DialogPrimitive.Title>
          <CommandPaletteBody
            items={items}
            inputRef={inputRef}
            placeholder={placeholder}
            label={label}
            emptyLabel={emptyLabel}
            emptyHint={emptyHint}
            onDismiss={() => onOpenChange(false)}
          />
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}
