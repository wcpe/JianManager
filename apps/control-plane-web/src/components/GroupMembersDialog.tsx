import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useGroups, useAddGroupMember, useRemoveGroupMember } from '@/api/groups'
import { useUsers } from '@/api/users'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Button } from '@jianmanager/ui/components/button'

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
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader>
          <DialogTitle>{t('groups.manageMembers', { name: group?.name ?? '' })}</DialogTitle>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-4">
          <div className="space-y-2">
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
        </ScrollableDialogBody>

        <DialogFooter className="pt-4">
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
