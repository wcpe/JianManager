import { useState } from 'react'
import { useGroups, useAddGroupMember, useRemoveGroupMember } from '@/api/groups'
import { useUserSearch } from '@/api/users'
import { useDebounced } from '@/lib/use-debounced'
import { GroupMembersDialogView } from '@jianmanager/ui/components/views/groups/GroupMembersDialogView'

/** 候选默认窗口：只取前 50 条，靠键入服务端 q 缩小（FR-336）。请求上限属应用侧策略，故留在容器。 */
const CANDIDATE_LIMIT = 50

interface GroupMembersDialogProps {
  /** 目标组 ID（从实时 useGroups 读取，使增删成员即时反映）。 */
  groupId: number
  onClose: () => void
}

/**
 * 管理用户组成员对话框的应用接线层（ADR-097 b 范式）。
 *
 * 视图已受控；本层保留「何时发请求」这一策略：组数据取实时 `useGroups`（增删后即刻反映），
 * 候选走服务端搜索（FR-336）——默认前 N 条、键入经 300ms 防抖下发 q；增删经 mutation hook
 * 执行（成功/失败 toast 挂在 hook 层）。保留同路径默认导出与同一套 props，调用点无需改动。
 */
export default function GroupMembersDialog({ groupId, onClose }: GroupMembersDialogProps) {
  const { data: groups } = useGroups()
  const addMember = useAddGroupMember()
  const removeMember = useRemoveGroupMember()
  const [kw, setKw] = useState('')
  const q = useDebounced(kw, 300).trim()
  const { data: page } = useUserSearch({ q: q || undefined, limit: CANDIDATE_LIMIT })

  const group = groups?.find((g) => g.id === groupId)

  return (
    <GroupMembersDialogView
      groupName={group?.name}
      members={group?.members}
      candidates={page?.items}
      candidateTotal={page?.total}
      onQueryChange={setKw}
      onAdd={(userId) => addMember.mutate({ id: groupId, userId })}
      onRemove={(userId) => removeMember.mutate({ id: groupId, userId })}
      adding={addMember.isPending}
      removing={removeMember.isPending}
      onClose={onClose}
    />
  )
}
