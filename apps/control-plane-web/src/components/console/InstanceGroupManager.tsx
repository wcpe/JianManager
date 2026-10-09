import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { useInstances, useStartInstance, useStopInstance, useRestartInstance } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useInstanceGroups, useInstanceGroupSubtree, useAddInstanceGroupMembers, useRemoveInstanceGroupMembers } from '@/api/instanceGroups'
import { InstanceGroupTree } from './InstanceGroupTree'
import { InstanceGroupManager as InstanceGroupManagerView } from '@/components/views/instances/InstanceGroupManager'

/**
 * 实例分组管理页的应用接线层（ADR-097 b 范式）。
 *
 * 视图本体是受控视图（见 components/views）；本层持有选中组（子树查询依赖它，必须提到这一层）、
 * 取实例/节点/分组/子树、接两个成员动作、把提示交给 toast，并把已接线的分组树以函数插槽交回视图。
 * 保留同路径的具名导出，调用点无需改动。
 */
export function InstanceGroupManager() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [selectedGroupId, setSelectedGroupId] = useState<number | null>(null)

  const { data: allInstances } = useInstances()
  const { data: nodes } = useNodes()
  const { data: groups } = useInstanceGroups()
  // 选中组时取其子树（含后代、去重）的实例 ID 集合；未选时不查（显示全部）。
  const { data: subtreeIds } = useInstanceGroupSubtree(selectedGroupId)
  const addMembers = useAddInstanceGroupMembers()
  const removeMembers = useRemoveInstanceGroupMembers()
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()

  const nodeNameOf = (id: number) => nodes?.find((n) => n.id === id)?.name ?? t('console.unknownNode', { id })

  return (
    <InstanceGroupManagerView
      selectedGroupId={selectedGroupId}
      onSelectGroup={setSelectedGroupId}
      instances={allInstances ?? []}
      groups={groups ?? []}
      subtreeIds={subtreeIds ?? null}
      nodeNameOf={nodeNameOf}
      onOpenInstance={(id) => navigate(`/instances/${id}`)}
      onLifecycle={(action, instanceId) => {
        const m = action === 'start' ? start : action === 'stop' ? stop : restart
        m.mutate(instanceId)
      }}
      tree={({ selectedGroupId: gid, onSelect }) => (
        <InstanceGroupTree selectedGroupId={gid} onSelect={onSelect} />
      )}
      onAddMembers={(payload) =>
        addMembers.mutateAsync({ id: payload.groupId, instanceIds: payload.instanceIds })
      }
      onRemoveMembers={async (payload) => {
        await removeMembers.mutateAsync({ id: payload.groupId, instanceIds: payload.instanceIds })
      }}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
