// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做组取数、三个写动作、URL 深链解析/写回与成员对话框接线。
import { useSearchParams } from 'react-router'
import {
  useGroups,
  useCreateGroup,
  useUpdateGroup,
  useUpdateGroupQuota,
  useDeleteGroup,
} from '@/api/groups'
import GroupMembersDialog from '@/components/GroupMembersDialog'
import { useDangerPermission } from '@/lib/danger'
import {
  GroupsPageView,
  type GroupPanel,
} from '@/components/views/users/GroupsPageView'

/**
 * 用户组管理页容器（ADR-097 b 范式）：组列表取数、创建/编辑/删除三个写动作、平台级删除门禁
 * 与成员对话框的接线都在这里决定，列表、卡片与三个对话框交共享视图。
 *
 * 受控状态归容器：深链选中组与面板（`?group=<id>&panel=edit|members`）——它随路由变化、
 * 浏览器前进/后退须复原，故解析与写回都留在应用侧（包内不碰路由）。
 * 成员管理对话框的实现经插槽注入：候选默认窗口、键入防抖与服务端搜索是应用侧策略。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function GroupsPage() {
  const { data: groups, isLoading } = useGroups()
  const createGroup = useCreateGroup()
  const updateGroup = useUpdateGroup()
  const updateQuota = useUpdateGroupQuota()
  const deleteGroup = useDeleteGroup()

  // 选中组与打开的面板入 URL（FR-128 可寻址）：`?group=<id>` 深链某组、`&panel=edit|members` 打开对应面板；
  // 直接从 searchParams 派生，浏览器前进/后退可复原。创建/删除属瞬时动作，仍走视图本地 state。
  const [searchParams, setSearchParams] = useSearchParams()
  const activeGroupId = (() => {
    const n = Number(searchParams.get('group'))
    return Number.isFinite(n) && n > 0 ? n : null
  })()
  const panelParam = searchParams.get('panel')
  const activePanel: GroupPanel | null =
    panelParam === 'edit' || panelParam === 'members' ? panelParam : null

  const openPanel = (id: number, panel: GroupPanel) => {
    const next = new URLSearchParams(searchParams)
    next.set('group', String(id))
    next.set('panel', panel)
    setSearchParams(next)
  }
  const closePanel = () => {
    const next = new URLSearchParams(searchParams)
    next.delete('group')
    next.delete('panel')
    setSearchParams(next)
  }

  // 删除是平台级破坏操作：角色门禁在应用侧判定后注入（包内不持鉴权状态）。
  const { allowed: dangerAllowed } = useDangerPermission('platform')

  return (
    <GroupsPageView
      groups={groups}
      isLoading={isLoading}
      activeGroupId={activeGroupId}
      activePanel={activePanel}
      onOpenPanel={openPanel}
      onClosePanel={closePanel}
      dangerAllowed={dangerAllowed}
      creating={createGroup.isPending}
      updating={updateGroup.isPending || updateQuota.isPending}
      onCreateGroup={async (values) => {
        await createGroup.mutateAsync({ name: values.name, description: values.description })
      }}
      onUpdateGroup={async (id, values) => {
        // 按原有顺序先更新名称/描述、再更新配额（成功提示与缓存失效由 mutation hook 挂）。
        await updateGroup.mutateAsync({ id, name: values.name, description: values.description })
        await updateQuota.mutateAsync({ id, ...values.quota })
      }}
      onDeleteGroup={(id) => {
        deleteGroup.mutate(id)
      }}
      renderMembersDialog={({ groupId, onClose }) => (
        <GroupMembersDialog groupId={groupId} onClose={onClose} />
      )}
    />
  )
}
