import { useTranslation } from 'react-i18next'

import { cn } from '@jianmanager/ui'
import { RoleBadge } from '@/components/views/instances/InstanceTableParts'
import { INSTANCE_KIND_LABEL_KEY, instanceKindOf, type InstanceKind } from '@/lib/instances/instance-kind'

/**
 * 实例「身份」徽章组：**表格的身份列与卡片视图共用**。
 *
 * 【为什么抽出来】此前两处各画一套：
 * - 表格显示**本地化类型徽章（带配色）** + 角色 + 环境 + 自由标签；
 * - 卡片只显示 `inst.type` 的**原始值**（如 `minecraft_java`）拼接节点名。
 * 结果是同一台实例在列表里是「插件服 / 后端 / 生产」，在卡片里是一串内部标识符，
 * 用户在两视图之间对照时对不上。用户要求「类型标签对齐表格模式」，指的就是这里。
 *
 * 收成一个组件后，两处只能一致——将来加新徽章也只需改这一处。
 */
const KIND_CHIP_CLASS: Record<InstanceKind, string> = {
  plugin: 'bg-primary/10 text-primary',
  proxy: 'bg-status-info/15 text-status-info',
  binary: 'bg-muted text-muted-foreground',
}

export interface InstanceIdentityBadgesProps {
  /** 实例类型标识（如 minecraft_java），用于推导类别徽章。 */
  type: string
  /** 角色（backend / proxy / universal）。 */
  role: string
  /** 环境标签值（如 prod），空则不出徽章。 */
  env?: string
  /** 自由标签（已剔除 region:/zone:/env:/role: 等受控前缀）。 */
  freeTags?: string[]
  /** 是否显示类型徽章（对应列设置里的 type 开关）。 */
  showType?: boolean
  /** 是否显示角色徽章。 */
  showRole?: boolean
  /** 是否显示自由标签。 */
  showTags?: boolean
  /** 自由标签最多显示几个（表格 2 个，卡片可给同样值以保持一致）。 */
  maxTags?: number
  /** 布局方向：表格用 row（紧凑单行），卡片可用 wrap（允许换行）。 */
  layout?: 'row' | 'wrap'
  /** 附加 className（例如卡片里给一点上边距）。 */
  className?: string
}

export function InstanceIdentityBadges({
  type,
  role,
  env,
  freeTags = [],
  showType = true,
  showRole = true,
  showTags = true,
  maxTags = 2,
  layout = 'row',
  className,
}: InstanceIdentityBadgesProps) {
  const { t } = useTranslation()
  const kind = instanceKindOf({ type, role } as Parameters<typeof instanceKindOf>[0])

  const hasTags = showTags && freeTags.length > 0

  return (
    <div className={cn('min-w-0', layout === 'wrap' ? 'flex flex-wrap items-center gap-1' : 'flex items-center gap-1', className)}>
      {showType && (
        <span
          className={cn(
            'inline-flex shrink-0 items-center rounded px-1.5 py-px text-[11px] font-medium',
            KIND_CHIP_CLASS[kind],
          )}
          title={type}
        >
          {t(INSTANCE_KIND_LABEL_KEY[kind])}
        </span>
      )}
      {showRole && <RoleBadge role={role} />}
      {env && (
        <span className="shrink-0 rounded border border-primary/40 px-1.5 py-px text-[11px] text-primary">
          {t(`grouping.env_${env}`, { defaultValue: env })}
        </span>
      )}
      {hasTags &&
        freeTags.slice(0, maxTags).map((tag) => (
          <span
            key={tag}
            className="max-w-[74px] truncate rounded bg-muted px-1.5 py-px text-[11px] text-muted-foreground"
          >
            {tag}
          </span>
        ))}
      {hasTags && freeTags.length > maxTags && (
        <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground/70">
          +{freeTags.length - maxTags}
        </span>
      )}
    </div>
  )
}
