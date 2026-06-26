import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Users, Server, Bot, HardDrive } from 'lucide-react'
import { useGroups, useDeleteGroup, type GroupInfo } from '@/api/groups'
import CreateGroupDialog from '@/components/CreateGroupDialog'
import GroupEditDialog from '@/components/GroupEditDialog'
import GroupMembersDialog from '@/components/GroupMembersDialog'
import DangerConfirm from '@/components/DangerConfirm'
import { Button } from '@/components/ui/button'
import { Panel } from '@/components/ui/panel'

export default function GroupsPage() {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  const [editGroup, setEditGroup] = useState<GroupInfo | null>(null)
  const [membersGroupId, setMembersGroupId] = useState<number | null>(null)
  const [deleteGroup, setDeleteGroup] = useState<{ id: number; name: string } | null>(null)
  const { data: groups, isLoading } = useGroups()
  const del = useDeleteGroup()

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">{t('groups.title')}</h1>
        <Button onClick={() => setShowCreate(true)}>+ {t('groups.createGroup')}</Button>
      </div>

      <CreateGroupDialog open={showCreate} onClose={() => setShowCreate(false)} />

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="space-y-2.5">
          {groups?.map((g) => (
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
                  <Button variant="ghost" size="xs" onClick={() => setEditGroup(g)}>
                    {t('common.edit')}
                  </Button>
                  <Button variant="ghost" size="xs" onClick={() => setMembersGroupId(g.id)}>
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
                    value={`${g.quota.maxStorageMb}MB`}
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
          {(!groups || groups.length === 0) && (
            <p className="text-muted-foreground text-center py-8">{t('groups.empty')}</p>
          )}
        </div>
      )}

      {editGroup && (
        <GroupEditDialog key={editGroup.id} group={editGroup} onClose={() => setEditGroup(null)} />
      )}
      {membersGroupId !== null && (
        <GroupMembersDialog groupId={membersGroupId} onClose={() => setMembersGroupId(null)} />
      )}
      <DangerConfirm
        open={deleteGroup !== null}
        title={t('danger.deleteGroupTitle', { name: deleteGroup?.name ?? '' })}
        description={t('danger.deleteGroupDesc')}
        confirmLabel={t('common.delete')}
        confirmText={deleteGroup?.name}
        scope="platform"
        onConfirm={() => { if (deleteGroup) del.mutate(deleteGroup.id); setDeleteGroup(null) }}
        onCancel={() => setDeleteGroup(null)}
      />
    </div>
  )
}

/** 配额小药丸（图标 + 标签 + 值），用户组属性的紧凑展示。 */
function QuotaChip({ icon, label, value }: { icon: React.ReactNode; label: string; value: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border bg-card px-2.5 py-1 text-xs text-muted-foreground">
      <span className="text-primary">{icon}</span>
      <span>{label}</span>
      <span className="font-medium text-foreground tabular-nums">{value}</span>
    </span>
  )
}
