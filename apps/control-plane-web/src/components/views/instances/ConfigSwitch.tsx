import { Switch } from '@jianmanager/ui/components/switch'

/**
 * 启用开关（受控）：列表行里的「开/关」语义控件。
 *
 * 实现已收敛到组件库的 {@link Switch}（Radix 原语）：此前这里手写了一整套
 * button[role=switch] + 轨道/滑块 span 与位移 class，和组件库的 Switch 表达同一件事，
 * 两套焦点环、禁用态与过渡时长各写各的。对外契约（checked / onChange(next) / disabled /
 * role=switch / aria-checked）保持不变，调用方无需改动。
 *
 * `onLabel` / `offLabel` 仍走原生 title：它们是随状态变化的悬停提示，不是纯装饰标签，
 * 而 Tooltip 需要额外 Provider 且内容是静态文本，此处用原生 title 更贴合。
 */
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
    <Switch
      checked={checked}
      disabled={disabled}
      aria-label={label}
      title={checked ? onLabel : offLabel}
      onCheckedChange={(next) => onChange(next)}
    />
  )
}
