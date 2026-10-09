import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from '@jianmanager/ui/components/sheet'
import { UnifiedDiff } from '@/components/views/UnifiedDiff'
import DangerConfirm from '@/components/views/DangerConfirm'
import type { FileVersion, FileVersionDiff } from '@/lib/file-version'

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type VersionNotice = (kind: 'success' | 'error', message: string) => void

/** 历史版本抽屉（FR-070）：版本列表 / diff / 一键回滚，复用 FR-051 后端，回滚走 DangerConfirm。 */
export interface VersionDrawerProps {
  /** 选中文件完整相对路径；为 null 时抽屉不渲染内容。 */
  filePath: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 回滚成功后回调，供父组件刷新文件内容。 */
  onRolledBack?: () => void
  /** 版本列表（外壳取数）。 */
  versions?: FileVersion[]
  versionsLoading: boolean
  versionsFailed: boolean
  /**
   * diff 选择变化（外壳据此取 diff）。视图内点「对比」/「到」会触发；
   * 两者都选定且不同时外壳才实际发请求，与原 `enabled` 条件一致。
   */
  onDiffSelect: (from: number | null, to: number | null) => void
  /** 两版本 diff 结果（外壳取数）。 */
  diff?: FileVersionDiff
  diffLoading: boolean
  /** diff 取数失败时的错误文案（外壳从 error 取 message）。 */
  diffError?: string
  /** 回滚到指定版本；失败请抛错，视图取服务端 message 提示。 */
  onRollback: (versionId: number) => Promise<void>
  /** 回滚进行中（禁用按钮）。 */
  rollbackPending: boolean
  /** 提示通道。 */
  notify: VersionNotice
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/**
 * 受控视图（ADR-097 c 范式）：版本列表、diff 与回滚三个取数全部由外壳注入。
 * 视图内只留「选中哪两个版本对比」「要回滚哪个版本」这两处纯 UI 状态。
 */
export default function VersionDrawer({
  filePath,
  open,
  onOpenChange,
  onRolledBack,
  versions: input,
  versionsLoading,
  versionsFailed,
  onDiffSelect,
  diff,
  diffLoading,
  diffError,
  onRollback,
  rollbackPending,
  notify,
}: VersionDrawerProps) {
  const { t } = useTranslation()
  const [diffFrom, setDiffFrom] = useState<number | null>(null)
  const [diffTo, setDiffTo] = useState<number | null>(null)
  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)
  const versions = input ?? []

  const pickFrom = (id: number) => {
    setDiffFrom(id)
    onDiffSelect(id, diffTo)
  }
  const pickTo = (id: number) => {
    setDiffTo(id)
    onDiffSelect(diffFrom, id)
  }

  const confirmRollback = async () => {
    if (rollbackTarget == null) return
    const target = rollbackTarget
    setRollbackTarget(null)
    try {
      await onRollback(target)
      notify('success', t('fileVersions.rollbackSuccess', { version: target }))
      onRolledBack?.()
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg || t('fileVersions.rollbackFailed'))
    }
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
            {versionsLoading ? (
              <p className="p-3 text-xs text-muted-foreground">{t('files.loading')}</p>
            ) : versionsFailed ? (
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
                          onClick={() => pickFrom(v.id)}
                        >
                          {t('fileVersions.diffFrom')}
                        </button>
                        <button
                          type="button"
                          className={diffTo === v.id ? 'font-semibold text-primary' : 'text-primary hover:underline'}
                          onClick={() => pickTo(v.id)}
                        >
                          {t('fileVersions.diffTo')}
                        </button>
                        {/* 回滚按钮禁用时屏蔽指针事件，否则 hover:underline 仍会触发下划线，看着像能点。 */}
                        <button
                          type="button"
                          className="text-amber-600 hover:underline disabled:pointer-events-none disabled:opacity-50"
                          disabled={rollbackPending}
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
              {diffLoading ? (
                <p className="text-xs">{t('files.loading')}</p>
              ) : diff?.binary ? (
                <p className="text-xs text-muted-foreground">{t('fileVersions.binary')}</p>
              ) : diff ? (
                <UnifiedDiff diff={diff.unifiedDiff} />
              ) : (
                <p className="text-xs text-destructive">{diffError ?? ''}</p>
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
        onConfirm={() => void confirmRollback()}
        onCancel={() => setRollbackTarget(null)}
      />
    </>
  )
}
