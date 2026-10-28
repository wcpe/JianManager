import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus } from 'lucide-react'
import { Button } from '@jianmanager/ui'
import { InstancePicker } from '@/components/instances/InstancePicker'
import type { PageContextEntityRef } from '@/lib/assistant/page-context'

/**
 * 「添加实体」入口（FR-518 §5.3）。
 *
 * 芯片本身的渲染在 `AssistantChatPanel` 里（与输入框同一行）；这里只负责
 * **手动搜索并追加一个实例实体**。拆开而不是把芯片也搬过来，是为了避免同一份
 * 芯片出现两套渲染——那种分叉一旦发生，两边样式与移除行为会慢慢走偏。
 *
 * 关键约束（护墙）：千级实例**必须走服务端搜索**，不得全量列举。
 * `components/instances/InstancePicker` 内部已接 `useInstanceSearch` + 300ms 防抖，
 * 候选有界（CANDIDATE_LIMIT）并在截断时提示，因此这里直接复用它，不自己拉列表。
 */

export interface AddEntityButtonProps {
  /** 已附加的实体（用于去重与上限判定）。 */
  entities: PageContextEntityRef[]
  onAdd: (entity: PageContextEntityRef) => void
  /** 实体上限（spec §4.1 至多 8 个）。 */
  maxEntities?: number
}

export function AddEntityButton({ entities, onAdd, maxEntities = 8 }: AddEntityButtonProps) {
  const { t } = useTranslation()
  const [picking, setPicking] = useState(false)
  const atLimit = entities.length >= maxEntities

  if (atLimit) {
    // 到上限时**不隐藏**而是说明原因：直接消失会让用户反复找「按钮哪去了」。
    return (
      <span className="px-1 text-[11px] text-muted-foreground">
        {t('assistant.chipLimit', '最多附加 {{max}} 个实体', { max: maxEntities })}
      </span>
    )
  }

  if (!picking) {
    return (
      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="h-6 gap-1 rounded-full px-2 text-[11px]"
        // 可访问名落在按钮本身（护墙：不得只加在外层容器，否则 getByLabelText 命中容器）。
        aria-label={t('assistant.chipAdd', '添加实体')}
        onClick={() => setPicking(true)}
      >
        <Plus className="size-3" />
        {t('assistant.chipAdd', '添加实体')}
      </Button>
    )
  }

  return (
    <span className="flex items-center gap-1 px-1">
      <InstancePicker
        enabled
        className="w-56"
        placeholder={t('assistant.chipPickInstance', '搜索实例…')}
        ariaLabel={t('assistant.chipPickInstance', '搜索实例…')}
        onChange={(_id, instance) => {
          if (!instance) return
          // 去重：同一实例重复添加没有意义，反而会让芯片看起来「点了没反应」。
          if (entities.some((e) => e.type === 'instance' && String(e.id) === String(instance.id))) {
            setPicking(false)
            return
          }
          onAdd({ type: 'instance', id: instance.id })
          setPicking(false)
        }}
      />
      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="h-7 px-2 text-[11px]"
        onClick={() => setPicking(false)}
      >
        {t('common.cancel', '取消')}
      </Button>
    </span>
  )
}
