import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from '@/components/ui/sheet'
import DangerConfirm from '@/components/DangerConfirm'
import {
  useConfigVersions,
  useConfigDiff,
  useRollbackConfig,
  type ConfigVersion,
} from '@/api/configs'

/**
 * 配置版本抽屉（FR-071）：版本列表 / diff / 一键回滚，复用 FR-031 配置版本端点
 * （`instance_config_versions`，与 FR-070 文件版本表区分）。回滚走 DangerConfirm，
 * 成功后 `useRollbackConfig` 失效 `['configs', instanceId]` 缓存，配置编辑器经 React Query 自动重载。
 */
interface ConfigVersionDrawerProps {
  instanceId: number
  /** 当前查看版本的文件相对路径；null 时不渲染内容。 */
  filePath: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 回滚成功后回调。 */
  onRolledBack: () => void
}

export default function ConfigVersionDrawer({
  instanceId,
  filePath,
  open,
  onOpenChange,
  onRolledBack,
}: ConfigVersionDrawerProps) {
  const { t } = useTranslation()
  const [diffFrom, setDiffFrom] = useState<number | null>(null)
  const [diffTo, setDiffTo] = useState<number | null>(null)
  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)

  const versionsQ = useConfigVersions(instanceId, open ? filePath : null)
  const diffQ = useConfigDiff(instanceId, filePath, diffFrom ?? undefined, diffTo ?? undefined)
  const rollbackMut = useRollbackConfig(instanceId, filePath)
  const versions: ConfigVersion[] = versionsQ.data ?? []

  const confirmRollback = () => {
    if (rollbackTarget == null) return
    const target = rollbackTarget
    rollbackMut.mutate(
      { versionId: target, message: `回滚到 #${target}` },
      {
        onSuccess: () => {
          setRollbackTarget(null)
          onRolledBack()
        },
        onError: () => setRollbackTarget(null),
      },
    )
  }

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>{t('configExplorer.versions')}</SheetTitle>
            <SheetDescription className="truncate font-mono text-xs">{filePath}</SheetDescription>
          </SheetHeader>

          <div className="flex-1 overflow-auto">
            {versionsQ.isLoading ? (
              <p className="p-3 text-xs text-muted-foreground">{t('common.loading')}</p>
            ) : versionsQ.error ? (
              <p className="p-3 text-xs text-destructive">{t('fileVersions.loadFailed')}</p>
            ) : versions.length === 0 ? (
              <p className="p-3 text-xs text-muted-foreground">{t('configExplorer.noVersions')}</p>
            ) : (
              <ul className="space-y-1">
                {versions.map((v) => (
                  <li key={v.id} className="rounded border px-3 py-2 text-xs hover:bg-muted/30">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-medium">#{v.id}</span>
                      <div className="flex shrink-0 gap-2">
                        <button
                          type="button"
                          className={diffFrom === v.id ? 'font-semibold text-primary' : 'text-primary hover:underline'}
                          onClick={() => setDiffFrom(v.id)}
                        >
                          {t('configExplorer.diffFrom')}
                        </button>
                        <button
                          type="button"
                          className={diffTo === v.id ? 'font-semibold text-primary' : 'text-primary hover:underline'}
                          onClick={() => setDiffTo(v.id)}
                        >
                          {t('configExplorer.diffTo')}
                        </button>
                        <button
                          type="button"
                          className="text-amber-600 hover:underline disabled:opacity-50"
                          disabled={rollbackMut.isPending}
                          onClick={() => setRollbackTarget(v.id)}
                        >
                          {t('configExplorer.rollback')}
                        </button>
                      </div>
                    </div>
                    <div className="mt-0.5 truncate text-muted-foreground">
                      {v.message || t('configExplorer.noMessage')}
                      {v.rollbackOfVersionId ? (
                        <span className="ml-2 text-amber-600">← #{v.rollbackOfVersionId}</span>
                      ) : null}
                    </div>
                    <div className="text-[10px] text-muted-foreground">{new Date(v.createdAt).toLocaleString()}</div>
                  </li>
                ))}
              </ul>
            )}
          </div>

          {diffFrom != null && diffTo != null && diffFrom !== diffTo && (
            <div className="mt-2 max-h-48 overflow-auto rounded border bg-muted/30 p-2">
              <div className="mb-1 text-xs font-medium">
                {t('configExplorer.diffTitle')} #{diffFrom} → #{diffTo}
              </div>
              {diffQ.isLoading ? (
                <p className="text-xs">{t('common.loading')}</p>
              ) : diffQ.data ? (
                <pre className="whitespace-pre-wrap font-mono text-[10px]">{diffQ.data.unifiedDiff}</pre>
              ) : (
                <p className="text-xs text-destructive">{diffQ.error ? (diffQ.error as Error).message : ''}</p>
              )}
            </div>
          )}
        </SheetContent>
      </Sheet>

      <DangerConfirm
        open={rollbackTarget != null}
        title={t('configExplorer.rollbackTitle')}
        description={t('configExplorer.rollbackConfirm', { name: filePath ?? '', version: rollbackTarget ?? 0 })}
        confirmLabel={t('configExplorer.rollback')}
        onConfirm={confirmRollback}
        onCancel={() => setRollbackTarget(null)}
      />
    </>
  )
}
