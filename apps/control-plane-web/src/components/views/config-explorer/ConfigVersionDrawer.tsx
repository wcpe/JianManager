/**
 * @file ConfigVersionDrawer：配置版本抽屉（版本列表 / diff / 回滚二次确认）的受控视图，三个取数与回滚请求由应用容器负责。
 * @input lib/config-contracts（ConfigVersion/ConfigDiff）、views/UnifiedDiff（FR-141 diff 着色）、
 *        views/DangerConfirm（回滚二次确认）、Sheet 原语、翻译上下文
 * @output ConfigVersionDrawer、ConfigVersionDrawerProps
 * @sync apps/control-plane-web/src/components/config-explorer/ConfigVersionDrawer.tsx、
 *        apps/control-plane-web/src/components/config-explorer/ConfigVersionDrawer.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-071 配置浏览器历史、复用 FR-031 配置版本端点）
 */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from '@jianmanager/ui/components/sheet'
import { UnifiedDiff } from '@/components/views/common/UnifiedDiff'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import type { ConfigDiff, ConfigVersion } from '@/lib/config-explorer/config-contracts'

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 版本列表、diff 与回滚请求全部经 props 注入 / 上报（容器调 `useConfigVersions` / `useConfigDiff` / `useRollbackConfig`）；
 * - **上提容器**的状态：对比起止 `diffFrom`/`diffTo`——它们是 diff 查询的键，一变就触发取数，
 *   故由容器唯一持有，本视图只渲染并回传选择（`onDiffSelect`），不持本地镜像；
 * - **留本视图**的状态：待回滚目标 `rollbackTarget`（二次确认的草稿，不触发取数）及其开合；
 * - 回滚**失败不额外提示**：提示由应用侧 mutation hook 统一负责（原实现同样只关闭确认弹窗），
 *   视图内重复提示会造成双重 toast；
 * - 回滚确认弹窗**不带** `scope`/`allowed`：与原实现逐字一致——本操作不做前端角色门禁；
 *   门禁语义不得在此隐式新增或放宽，要加须先改契约（`DangerConfirm` 的 `scope` + 容器注入 `allowed`）。
 */
export interface ConfigVersionDrawerProps {
  /** 当前查看版本的文件相对路径；null 时不渲染内容（容器也不取数）。 */
  filePath: string | null
  /** 抽屉开合（归调用方：资源管理器持有打开态）。 */
  open: boolean
  /** 开合变更上报。 */
  onOpenChange: (open: boolean) => void
  /** 回滚成功后回调（容器据此刷新配置编辑器内容）。 */
  onRolledBack: () => void
  /** 版本列表（容器取数）。 */
  versions?: ConfigVersion[]
  /** 版本列表加载态。 */
  versionsLoading: boolean
  /** 版本列表取数失败态（渲染失败文案而非崩溃）。 */
  versionsFailed: boolean
  /** 对比起始版本；null 表示未选。 */
  diffFrom: number | null
  /** 对比目标版本；null 表示未选。 */
  diffTo: number | null
  /** 对比起止选择上报（容器持有该状态并驱动 diff 取数）。 */
  onDiffSelect: (from: number | null, to: number | null) => void
  /** 两版本之间的 diff 结果（容器取数）。 */
  diff?: ConfigDiff
  /** diff 加载态。 */
  diffLoading: boolean
  /** diff 取数失败原因（容器从 error 取 message）。 */
  diffError?: string
  /** 回滚到指定版本；失败请抛错或静默（提示由容器侧 mutation 负责）。 */
  onRollback: (versionId: number) => Promise<void>
  /** 回滚在途：禁用版本行上的回滚按钮。 */
  rollbackPending: boolean
}

/**
 * 配置版本抽屉（FR-071 历史）：版本列表 / 起止对比 / 一键回滚。
 * 复用 FR-031 配置版本端点（`instance_config_versions`，与 FR-070 文件版本表区分）。
 */
export default function ConfigVersionDrawer({
  filePath,
  open,
  onOpenChange,
  onRolledBack,
  versions: input,
  versionsLoading,
  versionsFailed,
  diffFrom,
  diffTo,
  onDiffSelect,
  diff,
  diffLoading,
  diffError,
  onRollback,
  rollbackPending,
}: ConfigVersionDrawerProps) {
  const { t } = useTranslation()
  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)
  const versions: ConfigVersion[] = input ?? []

  const confirmRollback = async () => {
    if (rollbackTarget == null) return
    const target = rollbackTarget
    setRollbackTarget(null)
    try {
      await onRollback(target)
      onRolledBack()
    } catch {
      // 失败静默收窗：提示由容器侧 mutation 统一给出，此处不重复弹。
    }
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
            {versionsLoading ? (
              <p className="p-3 text-xs text-muted-foreground">{t('common.loading')}</p>
            ) : versionsFailed ? (
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
                          onClick={() => onDiffSelect(v.id, diffTo)}
                        >
                          {t('configExplorer.diffFrom')}
                        </button>
                        <button
                          type="button"
                          className={diffTo === v.id ? 'font-semibold text-primary' : 'text-primary hover:underline'}
                          onClick={() => onDiffSelect(diffFrom, v.id)}
                        >
                          {t('configExplorer.diffTo')}
                        </button>
                        {/* 回滚按钮禁用时屏蔽指针事件，否则 hover:underline 仍会触发下划线，看着像能点。 */}
                        <button
                          type="button"
                          className="text-amber-600 hover:underline disabled:pointer-events-none disabled:opacity-50"
                          disabled={rollbackPending}
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
              {diffLoading ? (
                <p className="text-xs">{t('common.loading')}</p>
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
        title={t('configExplorer.rollbackTitle')}
        description={t('configExplorer.rollbackConfirm', { name: filePath ?? '', version: rollbackTarget ?? 0 })}
        confirmLabel={t('configExplorer.rollback')}
        onConfirm={() => void confirmRollback()}
        onCancel={() => setRollbackTarget(null)}
      />
    </>
  )
}
