import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useGroups, useAddGroupMember, useRemoveGroupMember } from '@/api/groups'
import { useUsers } from '@/api/users'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { Button } from '@/components/ui/button'

interface GroupMembersDialogProps {
  /** 目标组 ID（从实时 useGroups 读取，使增删成员即时反映）。 */
  groupId: number
  onClose: () => void
}

/** 管理用户组成员：列出现有成员可移除 + 选择用户加入（FR-156，兑现 FR-003）。 */
export default function GroupMembersDialog({ groupId, onClose }: GroupMembersDialogProps) {
  const { t } = useTranslation()
  const { data: groups } = useGroups()
  const { data: users } = useUsers()
  const addMember = useAddGroupMember()
  const removeMember = useRemoveGroupMember()
  const [pick, setPick] = useState('')

  const group = groups?.find((g) => g.id === groupId)

  const memberUserIds = new Set((group?.members ?? []).map((m) => m.userId))
  const candidates: ComboboxOption[] = (users ?? [])
    .filter((u) => !memberUserIds.has(u.id))
    .map((u) => ({ value: String(u.id), label: u.username }))

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-md`}>
        <h2 className="text-lg font-bold mb-4">{t('groups.manageMembers', { name: group?.name ?? '' })}</h2>

        <div className="space-y-2 mb-4 max-h-60 overflow-y-auto">
          {(group?.members ?? []).map((m) => (
            <div key={m.id} className="flex items-center justify-between rounded border px-3 py-1.5 text-sm">
              <span>
                {m.user?.username ?? `${t('groups.userPrefix')}${m.userId}`}
                {m.role === 1 && <span className="ml-1 text-xs text-muted-foreground">({t('groups.admin')})</span>}
              </span>
              <Button
                variant="ghost"
                size="xs"
                className="text-red-600 hover:text-red-700"
                disabled={removeMember.isPending}
                onClick={() => removeMember.mutate({ id: groupId, userId: m.userId })}
              >
                {t('groups.removeMember')}
              </Button>
            </div>
          ))}
          {(group?.members ?? []).length === 0 && (
            <p className="text-sm text-muted-foreground">{t('groups.noMembers')}</p>
          )}
        </div>

        <div className="flex items-center gap-2">
          <div className="flex-1">
            <Combobox
              options={candidates}
              value={pick}
              onChange={setPick}
              allowCustom={false}
              placeholder={t('groups.selectUser')}
            />
          </div>
          <Button
            size="sm"
            disabled={!pick || addMember.isPending}
            onClick={() => {
              if (pick) {
                addMember.mutate({ id: groupId, userId: Number(pick) })
                setPick('')
              }
            }}
          >
            {t('groups.addMember')}
          </Button>
        </div>

        <div className="flex justify-end pt-4">
          <button onClick={onClose} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
            {t('common.close')}
          </button>
        </div>
      </div>
    </div>
  )
}
