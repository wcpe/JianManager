import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { instanceLifecyclePresentation, type InstanceLifecycleAction, type LifecycleInstanceLike } from './instance-lifecycle-status'

/** 同一套状态语义用于卡片、列表；原文可通过 title 查看，不撑大固定行盒。 */
export function InstanceLifecycleStatus({
  inst, provisioning = false, pendingAction, compact = false,
}: {
  inst: LifecycleInstanceLike
  provisioning?: boolean
  pendingAction?: InstanceLifecycleAction
  compact?: boolean
}) {
  const { t } = useTranslation()
  const presentation = instanceLifecyclePresentation(inst, { provisioning, pendingAction })
  const label = t(presentation.labelKey, { defaultValue: presentation.labelFallback })
  const detail = presentation.detailKey
    ? t(presentation.detailKey, { defaultValue: presentation.detail })
    : presentation.detail
  const fullText = detail ? `${label}：${detail}` : label
  return (
    <div
      className="min-w-0"
      data-testid="instance-lifecycle-status"
      data-lifecycle-kind={presentation.kind}
      title={fullText}
      aria-label={fullText}
      aria-busy={presentation.transitioning}
      tabIndex={detail ? 0 : undefined}
    >
      <StatusBadge
        level={presentation.tone}
        label={label}
        pulse={presentation.transitioning}
        dot={!compact}
        className={compact ? 'block max-w-full truncate px-1 py-0 text-[11px] leading-5' : 'bg-transparent px-0 py-0'}
      />
      {detail && (
        <p
          className={cn(
            'mt-0.5 overflow-hidden text-[11px] leading-4',
            compact ? 'truncate' : 'line-clamp-2',
            presentation.tone === 'danger' ? 'text-status-danger' : 'text-status-warning',
          )}
          style={compact ? undefined : { maxHeight: 32 }}
          data-testid="instance-lifecycle-detail"
        >
          {detail}
        </p>
      )}
    </div>
  )
}
