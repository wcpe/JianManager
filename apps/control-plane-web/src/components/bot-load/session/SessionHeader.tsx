import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { SessionHeaderView } from '@/components/views/bot-load/session/SessionHeaderView'
import type { BotLoadRunV2 } from '@/lib/bot-load/bot-load-types'
import { useCancelBotLoadRun, useDownloadBotLoadReport, useStopBotLoadRun } from '@/api/bot-load'

/**
 * 压测会话对象头：展示层已回迁应用侧（`SessionHeaderView`），此处提供导航与三个 mutation
 * （成功/失败提示也在容器侧，包内不依赖 sonner）。
 */
export function SessionHeader({
  run,
  streamStatus,
  reportReady,
}: {
  run: BotLoadRunV2
  streamStatus: string
  reportReady: boolean
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const stopMut = useStopBotLoadRun()
  const cancelMut = useCancelBotLoadRun()
  const downloadMut = useDownloadBotLoadReport()

  return (
    <SessionHeaderView
      run={run}
      streamStatus={streamStatus}
      reportReady={reportReady}
      onNavigate={navigate}
      onStop={() =>
        stopMut.mutate(
          { id: run.id },
          {
            onSuccess: () => toast.success(t('botLoad.stopAccepted')),
            onError: () => toast.error(t('botLoad.actionFailed')),
          },
        )
      }
      onCancel={() =>
        cancelMut.mutate(
          { id: run.id },
          {
            onSuccess: () => toast.success(t('botLoad.cancelAccepted')),
            onError: () => toast.error(t('botLoad.actionFailed')),
          },
        )
      }
      onDownload={(format) =>
        downloadMut.mutate(
          { id: run.id, runUuid: run.uuid, format },
          {
            onSuccess: () => toast.success(t('botLoad.reportDownloaded', { format })),
            onError: () => toast.error(t('botLoad.reportFailed')),
          },
        )
      }
      stopPending={stopMut.isPending}
      cancelPending={cancelMut.isPending}
      downloadPending={downloadMut.isPending}
    />
  )
}
