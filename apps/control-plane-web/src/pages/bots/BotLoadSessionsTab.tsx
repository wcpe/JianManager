import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useBotStressSessions, useStartBotStressSession, useStopBotStressSession } from '@/api/bots'
import { mergeSearchParams, readSessionsFilter } from '@/lib/bot-load-url-state'
import BotLoadWizard from '@/components/bot-load/BotLoadWizard'
import { SESSIONS_TAB_PAGE_SIZE, SessionsTabView } from '@/components/views/bot-load/SessionsTabView'

/**
 * 压测会话列表 tab 的容器：取数、启停 mutation/toast、分页写 URL 与详情路由。
 * 展示层已回迁应用侧（`SessionsTabView`，ADR-097）；详情页路由 `/bots/sessions/:id`
 * 由 FR-372 承接，此处仅列表与创建向导入口。
 */
export default function BotLoadSessionsTab() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const filter = readSessionsFilter(searchParams)
  const page = filter.page ?? 1
  const [wizardOpen, setWizardOpen] = useState(false)

  const sessions = useBotStressSessions({ page, pageSize: SESSIONS_TAB_PAGE_SIZE })
  const startSession = useStartBotStressSession()
  const stopSession = useStopBotStressSession()

  const run = (kind: 'start' | 'stop', id: number) => {
    const mutation = kind === 'start' ? startSession : stopSession
    mutation.mutate(id, {
      onError: () => toast.error(t('bots.stressActionFailed')),
    })
  }

  return (
    <>
      <SessionsTabView
        data={sessions.data}
        page={page}
        isLoading={sessions.isLoading}
        isError={sessions.isError}
        onRefresh={() => {
          void sessions.refetch()
        }}
        onPageChange={(p) =>
          setSearchParams(mergeSearchParams(searchParams, { page: p <= 1 ? null : p }), { replace: true })
        }
        onOpenSession={(id) => navigate(`/bots/sessions/${id}?tab=overview`)}
        onCreateClick={() => setWizardOpen(true)}
        onStart={(id) => run('start', id)}
        onStop={(id) => run('stop', id)}
        startPending={startSession.isPending}
        stopPending={stopSession.isPending}
        initialSearch={filter.q ?? ''}
      />

      <BotLoadWizard open={wizardOpen} onOpenChange={setWizardOpen} />
    </>
  )
}
