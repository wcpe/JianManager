import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useGroups } from '@/api/groups'
import CreateGroupDialog from '@/components/CreateGroupDialog'
import { Button } from '@/components/ui/button'

export default function GroupsPage() {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  const { data: groups, isLoading } = useGroups()

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
        <div className="space-y-4">
          {groups?.map((g) => (
            <div key={g.id} className="border rounded-lg p-4">
              <div className="flex items-center justify-between mb-2">
                <h3 className="font-medium text-lg">{g.name}</h3>
              </div>
              {g.description && <p className="text-sm text-muted-foreground mb-3">{g.description}</p>}
              <div className="flex gap-4 text-sm">
                <span>{t('groups.members')}: {g.members?.length ?? 0}</span>
                {g.quota && (
                  <>
                    <span>{t('groups.instanceQuota')}: {g.quota.maxInstances}</span>
                    <span>{t('groups.botQuota')}: {g.quota.maxBots}</span>
                    <span>{t('groups.storageQuota')}: {g.quota.maxStorageMb}MB</span>
                  </>
                )}
              </div>
              {g.members && g.members.length > 0 && (
                <div className="mt-2 flex gap-1 flex-wrap">
                  {g.members.map((m) => (
                    <span key={m.id} className="px-2 py-0.5 text-xs bg-muted rounded">
                      {m.user?.username ?? `${t('groups.userPrefix')}${m.userId}`}
                      {m.role === 1 && ` (${t('groups.admin')})`}
                    </span>
                  ))}
                </div>
              )}
            </div>
          ))}
          {(!groups || groups.length === 0) && (
            <p className="text-muted-foreground text-center py-8">{t('groups.empty')}</p>
          )}
        </div>
      )}
    </div>
  )
}
