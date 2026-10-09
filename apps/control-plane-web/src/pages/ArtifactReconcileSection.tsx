// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做对账设置/运行/差异的取数与处置动作接线。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useCleanupOrphans, useReconcileDiffs, useReconcileRuns, useReconcileSettings, useResolveMissing, useTriggerReconcile, useUpdateReconcileSettings } from '@/api/artifactReconcile'
import { ArtifactReconcileSectionView } from '@/components/views/artifacts/ArtifactReconcileSectionView'
import type { ArtifactReconcileRunView } from '@/components/views/artifacts/ArtifactReconcileSectionView'

/** 差异列表每页条数；同时是查询参数与分页器算页数的依据。 */
const DIFF_PAGE_SIZE = 50

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

export interface ArtifactChannelRef {
  id: number
  name: string
  type: string
}

/**
 * 制品 S3 一致性对账区块（FR-349）容器：设置、运行列表与两侧差异分页的取数，
 * 以及保存设置 / 立即运行 / 标记丢失 / 清理孤儿四个动作全部留在此处。
 *
 * 会触发重新取数的状态（选中哪次运行、缺失与孤儿各自的页码）归本层持有——视图只上报
 * 「打开某次运行」「翻到第 N 页」的意图；弹窗开合、设置草稿与待确认意图是纯 UI 状态，留视图内。
 * 保留同路径默认导出与同名 props，调用点（RuntimeAssetsPage）无需改动。
 */
export default function ArtifactReconcileSection({ channels }: { channels: ArtifactChannelRef[] }) {
  const { t } = useTranslation()
  const settings = useReconcileSettings()
  const runs = useReconcileRuns()
  const updateSettings = useUpdateReconcileSettings()
  const trigger = useTriggerReconcile()
  const resolve = useResolveMissing()
  const cleanup = useCleanupOrphans()

  // 报告弹窗：选中运行 + 两侧页码。runId 为 0 时差异查询自禁用（无报告打开）。
  // 存视图的窄契约类型即可：本层只取 id、并把同一对象回传给视图，无需 API 的完整字段。
  const [reportRun, setReportRun] = useState<ArtifactReconcileRunView | null>(null)
  const [missingPage, setMissingPage] = useState(1)
  const [orphanPage, setOrphanPage] = useState(1)
  const reportRunId = reportRun?.id ?? 0
  const missing = useReconcileDiffs(reportRunId, 'missing', missingPage, DIFF_PAGE_SIZE)
  const orphan = useReconcileDiffs(reportRunId, 'orphan', orphanPage, DIFF_PAGE_SIZE)

  return (
    <ArtifactReconcileSectionView
      channels={channels}
      runs={runs.data}
      settings={settings.data}
      settingsSaving={updateSettings.isPending}
      triggering={trigger.isPending}
      resolving={resolve.isPending}
      cleaning={cleanup.isPending}
      report={
        reportRun
          ? {
              run: reportRun,
              missing: { page: missingPage, pageSize: DIFF_PAGE_SIZE, data: missing.data },
              orphan: { page: orphanPage, pageSize: DIFF_PAGE_SIZE, data: orphan.data },
            }
          : null
      }
      onSaveSettings={async (payload) => {
        try {
          await updateSettings.mutateAsync(payload)
          toast.success(t('artifactReconcile.settingsSaved'))
          return true
        } catch (error) {
          toast.error(errMessage(error, t('artifactReconcile.actionFailed')))
          return false
        }
      }}
      onTrigger={() => {
        trigger.mutate(undefined, {
          onSuccess: (result) => toast.success(t('artifactReconcile.triggered', { started: result.started.length, skipped: result.skipped.length })),
          onError: (error) => toast.error(errMessage(error, t('artifactReconcile.actionFailed'))),
        })
      }}
      onOpenReport={(run) => {
        // 与原实现等价：报告组件每次打开都是新的一份，两侧页码从第 1 页起。
        setReportRun(run)
        setMissingPage(1)
        setOrphanPage(1)
      }}
      onCloseReport={() => setReportRun(null)}
      onMissingPageChange={setMissingPage}
      onOrphanPageChange={setOrphanPage}
      onResolveMissing={async () => {
        try {
          const result = await resolve.mutateAsync(reportRunId)
          toast.success(t('artifactReconcile.markedResult', result))
          return true
        } catch {
          // 与原实现一致：该动作失败不弹提示，仅关闭确认框（错误随后由列表/查询状态体现）。
          return false
        }
      }}
      onCleanupOrphans={async () => {
        try {
          const result = await cleanup.mutateAsync(reportRunId)
          toast.success(t('artifactReconcile.cleanedResult', result))
          return true
        } catch {
          // 与原实现一致：该动作失败不弹提示，仅关闭确认框。
          return false
        }
      }}
    />
  )
}
