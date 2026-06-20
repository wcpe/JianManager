import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useFileVersions,
  useFileVersionDiff,
  useRollbackFile,
  type FileVersion,
} from '@/api/fileVersions'

/** FileVersionPanel 在文件浏览器中展示某文件的历史版本，支持 diff 与一键回滚（FR-051）。 */
interface FileVersionPanelProps {
  /** 实例 ID */
  instanceId: number
  /** 选中文件的完整相对路径（含目录前缀） */
  filePath: string
  /** 回滚成功后回调，供父组件刷新文件内容 */
  onRolledBack?: () => void
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export default function FileVersionPanel({ instanceId, filePath, onRolledBack }: FileVersionPanelProps) {
  const { t } = useTranslation()
  const [diffFrom, setDiffFrom] = useState<number | null>(null)
  const [diffTo, setDiffTo] = useState<number | null>(null)
  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)

  const versionsQ = useFileVersions(instanceId, filePath)
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
    <div className="border-t flex flex-col max-h-64 min-h-[8rem]">
      <div className="px-3 py-1.5 bg-muted/50 text-xs font-medium border-b">
        {t('fileVersions.title')}
      </div>
      <div className="flex-1 overflow-auto">
        {versionsQ.isLoading ? (
          <p className="p-3 text-xs text-muted-foreground">{t('files.loading')}</p>
        ) : versionsQ.error ? (
          <p className="p-3 text-xs text-red-500">{t('fileVersions.loadFailed')}</p>
        ) : versions.length === 0 ? (
          <p className="p-3 text-xs text-muted-foreground">{t('fileVersions.empty')}</p>
        ) : (
          <ul>
            {versions.map((v) => (
              <li key={v.id} className="border-b px-3 py-1.5 text-xs hover:bg-muted/30">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-medium">#{v.id}</span>
                  <div className="flex gap-2 shrink-0">
                    <button
                      type="button"
                      className={`hover:underline ${diffFrom === v.id ? 'text-blue-600 font-semibold' : 'text-blue-600'}`}
                      onClick={() => setDiffFrom(v.id)}
                    >
                      {t('fileVersions.diffFrom')}
                    </button>
                    <button
                      type="button"
                      className={`hover:underline ${diffTo === v.id ? 'text-blue-600 font-semibold' : 'text-blue-600'}`}
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
                <div className="text-muted-foreground flex justify-between gap-2">
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

      {/* diff 区域 */}
      {diffFrom != null && diffTo != null && diffFrom !== diffTo && (
        <div className="border-t p-2 max-h-40 overflow-auto bg-muted/30">
          <div className="text-xs font-medium mb-1">
            {t('fileVersions.diffTitle')} #{diffFrom} → #{diffTo}
          </div>
          {diffQ.isLoading ? (
            <p className="text-xs">{t('files.loading')}</p>
          ) : diffQ.data?.binary ? (
            <p className="text-xs text-muted-foreground">{t('fileVersions.binary')}</p>
          ) : diffQ.data ? (
            <pre className="text-[10px] whitespace-pre-wrap font-mono">{diffQ.data.unifiedDiff}</pre>
          ) : (
            <p className="text-xs text-red-500">{diffQ.error ? (diffQ.error as Error).message : ''}</p>
          )}
        </div>
      )}

      {/* 回滚二次确认 */}
      {rollbackTarget != null && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border rounded-lg p-6 w-full max-w-sm shadow-lg">
            <h3 className="text-lg font-bold mb-2">{t('fileVersions.rollbackTitle')}</h3>
            <p className="text-sm text-muted-foreground mb-4">
              {t('fileVersions.rollbackConfirm', { name: filePath, version: rollbackTarget })}
            </p>
            <div className="flex justify-end gap-2">
              <button
                onClick={() => setRollbackTarget(null)}
                className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
              >
                {t('files.cancel')}
              </button>
              <button
                onClick={confirmRollback}
                disabled={rollbackMut.isPending}
                className="px-4 py-2 text-sm bg-amber-600 text-white rounded-md hover:bg-amber-700 disabled:opacity-50"
              >
                {t('fileVersions.rollback')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
