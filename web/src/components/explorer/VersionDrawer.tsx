import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from '@/components/ui/sheet'
import DangerConfirm from '@/components/DangerConfirm'
import {
  useFileVersions,
  useFileVersionDiff,
  useRollbackFile,
  type FileVersion,
} from '@/api/fileVersions'

/** 历史版本抽屉（FR-070）：版本列表 / diff / 一键回滚，复用 FR-051 后端，回滚走 DangerConfirm。 */
interface VersionDrawerProps {
  instanceId: number
  /** 选中文件完整相对路径；为 null 时抽屉不渲染内容。 */
  filePath: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 回滚成功后回调，供父组件刷新文件内容。 */
  onRolledBack?: () => void
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export default function VersionDrawer({
  instanceId,
  filePath,
  open,
  onOpenChange,
  onRolledBack,
}: VersionDrawerProps) {
  const { t } = useTranslation()
  const [diffFrom, setDiffFrom] = useState<number | null>(null)
  const [diffTo, setDiffTo] = useState<number | null>(null)
  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)

  const versionsQ = useFileVersions(instanceId, open ? filePath : null)
  const diffQ = useFileVersionDiff(instanceId, filePath, diffFrom ?? undefined, diffTo ?? undefined)
  const rollbackMut = useRollbackFile(instanceId, filePath)
  const versions: FileVersion[] = versionsQ.data ?? []

  const confirmRollback = () => {
    if (rollbackTarget == null) return
    const target = rollbackTarget
    rollbackMut.mutate(
      { versionId: target },
      {
        onSuccess: () => {
          toast.success(t('fileVersions.rollbackSuccess', { version: target }))
          setRollbackTarget(null)
          onRolledBack?.()
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          toast.error(err.response?.data?.message || t('fileVersions.rollbackFailed'))
          setRollbackTarget(null)
        },
      },
    )
  }

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>{t('fileVersions.title')}</SheetTitle>
            <SheetDescription className="truncate font-mono text-xs">{filePath}</SheetDescription>
          </SheetHeader>

          <div className="flex-1 overflow-auto">
            {versionsQ.isLoading ? (
              <p className="p-3 text-xs text-muted-foreground">{t('files.loading')}</p>
            ) : versionsQ.error ? (
              <p className="p-3 text-xs text-destructive">{t('fileVersions.loadFailed')}</p>
            ) : versions.length === 0 ? (
              <p className="p-3 text-xs text-muted-foreground">{t('fileVersions.empty')}</p>
            ) : (
              <ul className="space-y-1">
                {versions.map((v) => (
                  <li key={v.id} className="rounded border px-3 py-2 text-xs hover:bg-muted/30">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-medium">#{v.id}</span>
                      <div className="flex gap-2 shrink-0">
                        <button
                          type="button"
                          className={diffFrom === v.id ? 'font-semibold text-primary' : 'text-primary hover:underline'}
                          onClick={() => setDiffFrom(v.id)}
                        >
                          {t('fileVersions.diffFrom')}
                        </button>
                        <button
                          type="button"
                          className={diffTo === v.id ? 'font-semibold text-primary' : 'text-primary hover:underline'}
                          onClick={() => setDiffTo(v.id)}
                        >
                          {t('fileVersions.diffTo')}
                        </button>
                        <button
                          type="button"
                          className="text-amber-600 hover:underline disabled:opacity-50"
                          disabled={rollbackMut.isPending}
                          onClick={() => setRollbackTarget(v.id)}
                        >
                          {t('fileVersions.rollback')}
                        </button>
                      </div>
                    </div>
                    <div className="mt-0.5 flex justify-between gap-2 text-muted-foreground">
                      <span className="text-[10px]">{new Date(v.createdAt).toLocaleString()}</span>
                      <span className="text-[10px] shrink-0">{formatSize(v.size)}</span>
                    </div>
                    {v.rollbackOfVersionId ? (
                      <div className="text-[10px] text-amber-600">
                        {t('fileVersions.rollbackVia')} #{v.rollbackOfVersionId}
                      </div>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </div>

          {diffFrom != null && diffTo != null && diffFrom !== diffTo && (
            <div className="mt-2 max-h-48 overflow-auto rounded border bg-muted/30 p-2">
              <div className="mb-1 text-xs font-medium">
                {t('fileVersions.diffTitle')} #{diffFrom} → #{diffTo}
              </div>
              {diffQ.isLoading ? (
                <p className="text-xs">{t('files.loading')}</p>
              ) : diffQ.data?.binary ? (
                <p className="text-xs text-muted-foreground">{t('fileVersions.binary')}</p>
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
        title={t('fileVersions.rollbackTitle')}
        description={t('fileVersions.rollbackConfirm', { name: filePath ?? '', version: rollbackTarget ?? 0 })}
        confirmLabel={t('fileVersions.rollback')}
        onConfirm={confirmRollback}
        onCancel={() => setRollbackTarget(null)}
      />
    </>
  )
}
