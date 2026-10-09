/**
 * @file GroupsPageView：用户组管理页（组卡片列表 + 创建/编辑/成员/删除对话框）的受控视图，
 *       取数、写动作与 URL 深链解析/写回由应用容器负责。
 * @input views/groups/CreateGroupDialogView（新建组视图）、views/groups/GroupEditDialogView（编辑组视图）、
 *        views/DangerConfirm（删除二次确认）、lib/backup（formatSizeMb，沿用原页的配额格式化）、
 *        Button/Panel/布局层原语、翻译上下文
 * @output GroupsPageView、GroupsPageViewProps、GroupPanel、GroupRow、GroupMemberChip、GroupsMembersDialogArgs
 * @sync apps/control-plane-web/src/pages/GroupsPage.tsx、apps/control-plane-web/src/pages/GroupsPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-156 用户组、FR-128 可寻址面板、FR-336 成员候选服务端搜索）
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Users, Server, Bot, HardDrive } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import DangerConfirm from '@jianmanager/ui/components/views/DangerConfirm'
import {
  CreateGroupDialogView,
  type CreateGroupValues,
} from '@/components/views/groups/CreateGroupDialogView'
import {
  GroupEditDialogView,
  type GroupEditValues,
} from '@/components/views/groups/GroupEditDialogView'
import { formatSizeMb } from '@jianmanager/ui/lib/backup'

/** 用户组详情可打开的面板（FR-128 可寻址）：编辑属性 / 管理成员。 */
export type GroupPanel = 'edit' | 'members'

/** 组成员药丸展示所需字段（`user` 缺失时回退显示 `#userId`）。 */
export interface GroupMemberChip {
  id: number
  userId: number
  /** 1 = 组管理员（加管理员小标）。 */
  role: number
  user?: { username?: string }
}

/**
 * 用户组行（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/groups` 的 `GroupInfo`：容器直接传 API
 * 返回的完整对象也结构兼容，无需把该 API 类型迁进包。
 * `quota` 的各上限缺省按「0 = 不限」呈现（与后端一致）。
 */
export interface GroupRow {
  id: number
  name: string
  description?: string
  quota?: { maxInstances?: number; maxBots?: number; maxStorageMb?: number }
  members?: GroupMemberChip[]
}

/** 成员管理对话框插槽参数（`groupId` 由本视图按深链选中组给出，关闭写回由外壳负责）。 */
export interface GroupsMembersDialogArgs {
  groupId: number
  /** 关闭对话框：底部按钮、遮罩与 Esc 均走此回调（外壳据此清掉 URL 参数）。 */
  onClose: () => void
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast、不读写路由**。
 * - 组列表经 props 注入（容器调 `useGroups`）；
 * - 三个写动作（创建组、编辑组、删除组）以回调上报，由容器执行 mutation 并决定文案；
 *   创建/编辑失败请抛错，视图内对话框据此回显服务端 message；
 * - **归容器**的受控状态：深链选中组与面板（`?group=<id>&panel=edit|members`）——它随路由
 *   变化、浏览器前进/后退须复原，故解析与写回都在容器；容器读 `groups` 后把 id 与面板传下来，
 *   视图据「组是否真实存在」做兜底（深链到已删除组时视为未选）；
 * - **留本组件**的纯 UI 状态：创建弹窗开合、待删除确认目标（两者都是瞬时动作，不入 URL）；
 * - **鉴权状态一律由容器注入**：`dangerAllowed` 决定平台级删除是否放行，包内不读登录态；
 * - 成员管理对话框走 `renderMembersDialog` 插槽：候选默认窗口、键入防抖与服务端搜索是外壳策略，
 *   视图只决定「哪个组、何时打开」，实现由外壳注入（包内不依赖取数钩子）。
 */
export interface GroupsPageViewProps {
  /** 用户组列表；容器取数后注入（缺省或空数组渲染空态）。 */
  groups?: GroupRow[]
  /** 列表加载态。 */
  isLoading?: boolean
  /** 深链选中的组 id（容器从 `?group=` 解析；缺省/非法值表示未选）。 */
  activeGroupId?: number | null
  /** 深链打开的面板（容器从 `?panel=` 解析；缺省或非法值表示未打开）。 */
  activePanel?: GroupPanel | null
  /** 打开某组的编辑/成员面板（容器负责把 `group`/`panel` 写回 URL）。 */
  onOpenPanel: (id: number, panel: GroupPanel) => void
  /** 关闭面板（容器负责清掉 URL 参数）。 */
  onClosePanel: () => void
  /** 危险操作（删除用户组）是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  dangerAllowed: boolean
  /** 创建在途：禁用创建弹窗的提交按钮。 */
  creating?: boolean
  /** 编辑在途：禁用编辑弹窗的提交按钮（名称/描述与配额两次请求任一在途即为真）。 */
  updating?: boolean
  /** 创建用户组；失败请抛错，对话框取服务端 message 作内联提示。 */
  onCreateGroup: (values: CreateGroupValues) => Promise<void>
  /** 编辑用户组（名称/描述与配额一并上报，由容器决定拆成哪几个请求）；失败请抛错。 */
  onUpdateGroup: (id: number, values: GroupEditValues) => Promise<void>
  /** 删除已确认的组（二次确认已在本视图内完成）。 */
  onDeleteGroup: (id: number) => void
  /** 成员管理对话框插槽；实现由外壳注入（候选搜索的连接策略属应用侧）。 */
  renderMembersDialog: (args: GroupsMembersDialogArgs) => ReactNode
}

/**
 * 用户组管理页（FR-156，兑现 FR-003）：组卡片列出描述、配额与成员，可编辑/管理成员/删除；
 * 编辑与成员面板经 `?group=<id>&panel=edit|members` 深链（FR-128）。
 */
export function GroupsPageView({
  groups,
  isLoading = false,
  activeGroupId,
  activePanel,
  onOpenPanel,
  onClosePanel,
  dangerAllowed,
  creating = false,
  updating = false,
  onCreateGroup,
  onUpdateGroup,
  onDeleteGroup,
  renderMembersDialog,
}: GroupsPageViewProps) {
  const { t } = useTranslation()
  // 纯 UI 状态：创建弹窗开合、待删除确认目标。
  const [showCreate, setShowCreate] = useState(false)
  const [deleteGroup, setDeleteGroup] = useState<{ id: number; name: string } | null>(null)

  // 选中组须真实存在（含深链到已删除组的兜底），否则视为未选。
  const activeGroup = (groups ?? []).find((g) => g.id === activeGroupId) ?? null

  return (
    // 全量对齐：外壳与页头改用布局层原语（原为裸 <div>，无 data-page，迁移时补上）。
    <PageShell data-page="groups">
      <PageHeader
        title={t('groups.title')}
        actions={<Button onClick={() => setShowCreate(true)}>+ {t('groups.createGroup')}</Button>}
      />

      {/* 创建对话框为已归包的受控视图：开合留本组件（瞬时动作），创建经回调交由容器执行。 */}
      <CreateGroupDialogView
        open={showCreate}
        onClose={() => setShowCreate(false)}
        submitting={creating}
        onSubmit={onCreateGroup}
      />

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="space-y-2.5">
          {(groups ?? []).map((g) => (
            <Panel
              key={g.id}
              hoverable
              icon={<Users className="size-4" />}
              title={
                <span className="flex items-center gap-2 text-sm">
                  <span className="font-semibold text-foreground">{g.name}</span>
                  <span className="text-xs font-normal text-muted-foreground">
                    {t('groups.members')} {g.members?.length ?? 0}
                  </span>
                </span>
              }
              actions={
                <>
                  <Button variant="ghost" size="xs" onClick={() => onOpenPanel(g.id, 'edit')}>
                    {t('common.edit')}
                  </Button>
                  <Button variant="ghost" size="xs" onClick={() => onOpenPanel(g.id, 'members')}>
                    {t('groups.manageMembersBtn')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    className="text-status-danger hover:text-status-danger"
                    onClick={() => setDeleteGroup({ id: g.id, name: g.name })}
                  >
                    {t('common.delete')}
                  </Button>
                </>
              }
              bodyClassName="px-4 py-3"
            >
              {g.description && <p className="mb-3 text-sm text-muted-foreground">{g.description}</p>}

              {g.quota && (
                <div className="mb-3 flex flex-wrap gap-2">
                  <QuotaChip icon={<Server className="size-3" />} label={t('groups.instanceQuota')} value={g.quota.maxInstances} />
                  <QuotaChip icon={<Bot className="size-3" />} label={t('groups.botQuota')} value={g.quota.maxBots} />
                  <QuotaChip
                    icon={<HardDrive className="size-3" />}
                    label={t('groups.storageQuota')}
                    value={formatSizeMb(g.quota.maxStorageMb ?? 0)}
                  />
                </div>
              )}

              {g.members && g.members.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {g.members.map((m) => (
                    <span
                      key={m.id}
                      className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs"
                    >
                      {m.user?.username ?? `${t('groups.userPrefix')}${m.userId}`}
                      {m.role === 1 && (
                        <span className="rounded-full bg-primary/15 px-1.5 text-[10px] font-medium text-primary">
                          {t('groups.admin')}
                        </span>
                      )}
                    </span>
                  ))}
                </div>
              )}
            </Panel>
          ))}
          {(groups ?? []).length === 0 && (
            <p className="text-muted-foreground text-center py-8">{t('groups.empty')}</p>
          )}
        </div>
      )}

      {/* 编辑/成员面板：开关与选中目标来自容器解析的深链，关闭写回也在容器。 */}
      {activeGroup && activePanel === 'edit' && (
        <GroupEditDialogView
          key={activeGroup.id}
          group={activeGroup}
          submitting={updating}
          onClose={onClosePanel}
          onSubmit={(values) => onUpdateGroup(activeGroup.id, values)}
        />
      )}
      {activeGroup && activePanel === 'members' && renderMembersDialog({ groupId: activeGroup.id, onClose: onClosePanel })}

      {/* 删除二次确认：scope 固定 platform（与原页一致，不降级）、是否放行由容器注入。 */}
      <DangerConfirm
        open={deleteGroup !== null}
        title={t('danger.deleteGroupTitle', { name: deleteGroup?.name ?? '' })}
        description={t('danger.deleteGroupDesc')}
        confirmLabel={t('common.delete')}
        confirmText={deleteGroup?.name}
        scope="platform"
        allowed={dangerAllowed}
        onConfirm={() => {
          if (deleteGroup) onDeleteGroup(deleteGroup.id)
          setDeleteGroup(null)
        }}
        onCancel={() => setDeleteGroup(null)}
      />
    </PageShell>
  )
}

/** 配额小药丸（图标 + 标签 + 值），用户组属性的紧凑展示。 */
function QuotaChip({ icon, label, value }: { icon: ReactNode; label: string; value: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border bg-card px-2.5 py-1 text-xs text-muted-foreground">
      <span className="text-primary">{icon}</span>
      <span>{label}</span>
      <span className="font-medium text-foreground tabular-nums">{value}</span>
    </span>
  )
}

export default GroupsPageView
