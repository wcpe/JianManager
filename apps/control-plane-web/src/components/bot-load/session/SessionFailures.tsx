import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { SESSION_FAILURES_PAGE_SIZE, SessionFailures as SessionFailuresView } from '@/components/views/bot-load/session/SessionFailures'
import { useBotLoadFailures, useRetryBotLoadFailed } from '@/api/bot-load'
import { readFailureFilter, writeFailureFilter } from '@/lib/bot-load/bot-load-filters'
import { useSessionEvents } from './SessionEventProvider'

/**
 * 会话失败明细接线层：过滤条件在 URL 上，重试走 mutation 并把结果文案交回展示层，
 * 展示层已回迁应用侧。
 */
export function SessionFailures({ runId }: { runId: number | string }) {
  const { t } = useTranslation()
  const { run } = useSessionEvents()
  const [params, setParams] = useSearchParams()
  const filter = useMemo(() => readFailureFilter(params), [params])
  const [retryResult, setRetryResult] = useState<string | null>(null)
  const retryMut = useRetryBotLoadFailed()

  const query = useBotLoadFailures(runId, {
    page: filter.page,
    pageSize: SESSION_FAILURES_PAGE_SIZE,
    category: filter.category,
    errorCode: filter.errorCode,
    botUuid: filter.botUuid,
    executorNodeId: filter.node,
    stepId: filter.step,
  })

  const doRetry = (botUuids?: string[]) => {
    const requestId = crypto.randomUUID()
    retryMut.mutate(
      {
        id: runId,
        requestId,
        botUuids,
        errorCodes: filter.errorCode ? [filter.errorCode] : undefined,
      },
      {
        onSuccess: (res) => {
          setRetryResult(
            t('botLoad.retryResultDetail', {
              requested: res.requested,
              accepted: res.accepted,
              skipped: res.skipped,
              errors: res.errors.length,
            }),
          )
          if (res.errors.length) {
            toast.message(
              res.errors
                .slice(0, 5)
                .map((e) => `${e.botUuid ?? '?'}: ${e.errorCode} ${e.message}`)
                .join('\n'),
            )
          } else {
            toast.success(t('botLoad.retryAccepted'))
          }
        },
        onError: () => toast.error(t('botLoad.actionFailed')),
      },
    )
  }

  return (
    <SessionFailuresView
      filter={filter}
      onFilterChange={(partial) =>
        setParams(writeFailureFilter(params, { ...filter, ...partial }), { replace: true })
      }
      data={query.data}
      isLoading={query.isLoading}
      failureSummary={run?.failureSummary ?? {}}
      onRetry={doRetry}
      retryPending={retryMut.isPending}
      retryResult={retryResult}
    />
  )
}
