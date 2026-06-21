/**
 * 可编辑下拉框 Combobox（FR-072）：已知集下拉 + 允许自定义输入。
 *
 * 用于「系统可获取项」（节点 / JDK / 核心类型 / MC 版本 / 群组 / 代理 / 模板 / 厂商 / 架构…）：
 * 既能从下拉选已知项，也能直接键入自定义值。基于 Radix Popover + 受控输入，
 * 过滤/匹配逻辑复用纯函数 {@link "@/lib/combobox"}，主题随 shadcn token 自适应明暗。
 */
import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronsUpDownIcon, CheckIcon, PlusIcon } from 'lucide-react'
import { Popover as PopoverPrimitive } from 'radix-ui'
import { cn } from '@/lib/utils'
import {
  type ComboboxOption,
  optionLabel,
  filterOptions,
  isKnownValue,
  shouldOfferCustom,
} from '@/lib/combobox'

export type { ComboboxOption }

interface ComboboxProps {
  /** 已知选项集合。 */
  options: ComboboxOption[]
  /** 当前值（提交值）。 */
  value: string
  /** 值变更回调（选已知项或确认自定义输入时触发）。 */
  onChange: (value: string) => void
  /** 占位文案。 */
  placeholder?: string
  /** 是否允许自定义输入（默认 true）。为 false 时退化为可搜索下拉。 */
  allowCustom?: boolean
  /** 禁用。 */
  disabled?: boolean
  /** 校验失败态：触发器加 destructive 边框（aria-invalid）。 */
  invalid?: boolean
  /** 触发器额外类名。 */
  className?: string
  /** 触发器 id（配合 FieldLabel htmlFor）。 */
  id?: string
}

/**
 * 可编辑下拉框。点击展开下拉，输入即时过滤；允许自定义时列表底部提供「使用 "xxx"」入口，
 * 回车确认当前输入。空值时触发器显示 placeholder。
 */
export function Combobox({
  options,
  value,
  onChange,
  placeholder,
  allowCustom = true,
  disabled,
  invalid,
  className,
  id,
}: ComboboxProps) {
  const { t } = useTranslation()
  const [open, setOpen] = React.useState(false)
  const [query, setQuery] = React.useState('')
  const inputRef = React.useRef<HTMLInputElement>(null)

  const filtered = filterOptions(options, query)
  const offerCustom = allowCustom && shouldOfferCustom(filtered, query) && shouldOfferCustom(options, query)
  const known = isKnownValue(options, value)
  const display = known
    ? optionLabel(options.find((o) => o.value === value)!)
    : value

  const commit = (v: string) => {
    onChange(v)
    setQuery('')
    setOpen(false)
  }

  const onOpenChange = (next: boolean) => {
    setOpen(next)
    if (next) {
      setQuery('')
      // 展开后聚焦搜索框便于直接键入
      requestAnimationFrame(() => inputRef.current?.focus())
    }
  }

  const onInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      const q = query.trim()
      if (q === '') return
      // 回车优先选中唯一过滤项，否则在允许自定义时确认输入
      if (filtered.length === 1) commit(filtered[0].value)
      else if (allowCustom) commit(q)
    }
  }

  return (
    <PopoverPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <PopoverPrimitive.Trigger asChild>
        <button
          type="button"
          id={id}
          disabled={disabled}
          aria-invalid={invalid}
          data-slot="combobox-trigger"
          className={cn(
            "flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-transparent px-3 py-2 text-sm whitespace-nowrap shadow-xs transition-[color,box-shadow] outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:bg-input/30 dark:hover:bg-input/50 dark:aria-invalid:ring-destructive/40",
            className,
          )}
        >
          <span className={cn('truncate', !value && 'text-muted-foreground')}>
            {value ? display : placeholder}
          </span>
          <ChevronsUpDownIcon className="size-4 shrink-0 opacity-50" />
        </button>
      </PopoverPrimitive.Trigger>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          align="start"
          sideOffset={4}
          data-slot="combobox-content"
          className="z-50 w-(--radix-popover-trigger-width) origin-(--radix-popover-content-transform-origin) overflow-hidden rounded-md border bg-popover p-1 text-popover-foreground shadow-md data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95"
        >
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onInputKeyDown}
            placeholder={t('combobox.searchPlaceholder')}
            className="mb-1 h-8 w-full rounded-sm border-b bg-transparent px-2 text-sm outline-none placeholder:text-muted-foreground"
          />
          <div className="max-h-56 overflow-y-auto">
            {filtered.length === 0 && !offerCustom && (
              <p className="px-2 py-2 text-xs text-muted-foreground">{t('combobox.noResults')}</p>
            )}
            {filtered.map((opt) => (
              <button
                key={opt.value}
                type="button"
                onClick={() => commit(opt.value)}
                className="relative flex w-full cursor-default items-center gap-2 rounded-sm py-1.5 pr-8 pl-2 text-left text-sm outline-hidden select-none hover:bg-accent hover:text-accent-foreground"
              >
                <span className="truncate">{optionLabel(opt)}</span>
                {opt.value === value && (
                  <CheckIcon className="absolute right-2 size-4" />
                )}
              </button>
            ))}
            {offerCustom && (
              <button
                type="button"
                onClick={() => commit(query.trim())}
                className="flex w-full cursor-default items-center gap-2 rounded-sm py-1.5 pl-2 pr-2 text-left text-sm outline-hidden select-none hover:bg-accent hover:text-accent-foreground"
              >
                <PlusIcon className="size-4 shrink-0 opacity-70" />
                <span className="truncate">{t('combobox.useCustom', { value: query.trim() })}</span>
              </button>
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}
