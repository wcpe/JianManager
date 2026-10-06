import { cn } from '@jianmanager/ui'

/** 启用开关（受控）：复用告警页既有 toggle 样式，统一 role=switch + a11y。 */
export function ConfigSwitch({
  checked,
  onChange,
  disabled,
  label,
  onLabel,
  offLabel,
}: {
  checked: boolean
  onChange: (next: boolean) => void
  disabled?: boolean
  /** 无障碍标签（aria-label）。 */
  label: string
  /** 开/关状态的 title 文案（hover 提示）。 */
  onLabel?: string
  offLabel?: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={checked ? onLabel : offLabel}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors duration-300 ease-ios disabled:opacity-50',
        checked ? 'bg-primary' : 'bg-muted-foreground/30',
      )}
    >
      <span
        className={cn(
          'inline-block size-4 transform rounded-full bg-background shadow transition-transform duration-300 ease-ios',
          checked ? 'translate-x-4' : 'translate-x-0.5',
        )}
      />
    </button>
  )
}
