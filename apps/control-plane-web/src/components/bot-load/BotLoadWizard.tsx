import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { BotLoadWizard as BotLoadWizardView } from '@/components/views/bot-load/BotLoadWizard'
import { useBotLoadNodes, useCreateBotLoadRun, useCreateBotLoadRunFromTemplate, usePreflightBotLoadRun, useStartBotLoadRun, type BotLoadTemplate } from '@/api/botLoad'
import { InstancePicker } from '@/components/instances/InstancePicker'
import { draftTargetBots } from '@/lib/bot-load-draft'
import type { BotLoadWizardDraft } from '@/lib/bot-load-draft'

interface BotLoadWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 从模板启动时传入 */
  template?: BotLoadTemplate | null
}

/**
 * 五步创建向导接线层：草稿状态机与校验已回迁应用侧，此处注入四个 mutation、实例选择器、
 * 节点容量查询与导航。失败时把服务端 message 包成 Error 抛出，供展示层就地显示。
 */
export default function BotLoadWizard({ open, onOpenChange, template }: BotLoadWizardProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const createRun = useCreateBotLoadRun()
  const createFromTemplate = useCreateBotLoadRunFromTemplate()
  const preflight = usePreflightBotLoadRun()
  const startRun = useStartBotLoadRun()

  // 节点容量查询的启用条件由向导内的目标实例决定，故在容器侧记住它。
  const [targetInstanceId, setTargetInstanceId] = useState<number | null>(null)
  const nodesQuery = useBotLoadNodes(targetInstanceId, open && targetInstanceId !== null)

  /** 服务端 message 优先，包成 Error 交给展示层。 */
  const serverError = (err: unknown, fallback: string) => {
    const msg =
      err && typeof err === 'object' && 'response' in err
        ? (err as { response?: { data?: { message?: string } } }).response?.data?.message
        : undefined
    return new Error(msg || fallback)
  }

  const onEnsureRun = async (draft: BotLoadWizardDraft): Promise<number> => {
    // eslint-disable-next-line react-hooks/purity -- 仅在用户提交路径生成默认名，非 render 期
    const name = draft.name.trim() || `run-${Date.now()}`
    const base = {
      instanceId: draft.instanceId!,
      name,
      namePrefix: draft.namePrefix.trim(),
      config: {
        server: draft.config.server,
        port: draft.config.port,
        auth: 'offline' as const,
        version: draft.config.version,
      },
      executorNodeIds: draft.executorMode === 'manual' ? draft.executorNodeIds : undefined,
    }
    try {
      if (draft.templateId) {
        const run = await createFromTemplate.mutateAsync({
          id: draft.templateId,
          payload: {
            ...base,
            commandScheduleOverride: draft.commandSchedule,
            loadProfileOverride: draft.loadProfile,
            thresholdsOverride: draft.thresholds,
          },
        })
        return run.id
      }
      const run = await createRun.mutateAsync({
        ...base,
        count: draftTargetBots(draft),
        commandSchedule: draft.commandSchedule,
        loadProfile: draft.loadProfile,
        thresholds: draft.thresholds,
      })
      return run.id
    } catch (e: unknown) {
      throw serverError(e, t('botsLoad.saveRunFailed'))
    }
  }

  const onPreflight = async (args: { runId: number; executorNodeIds?: number[] }) => {
    try {
      // 视图以 runId 表达会话身份，API hook 的载荷键是 id——在此转换（此前直接透传
      // 会让请求路径落到 /stress-sessions/undefined/preflight）。
      return await preflight.mutateAsync({ id: args.runId, executorNodeIds: args.executorNodeIds })
    } catch (e: unknown) {
      throw serverError(e, t('botsLoad.preflightFailed'))
    }
  }

  const onStart = async (args: { runId: number; planToken: string }) => {
    try {
      return await startRun.mutateAsync({ id: args.runId, planToken: args.planToken })
    } catch (e: unknown) {
      throw serverError(e, t('botsLoad.startFailed'))
    }
  }

  return (
    <BotLoadWizardView
      open={open}
      onOpenChange={onOpenChange}
      template={template}
      InstancePicker={InstancePicker}
      nodes={nodesQuery.data}
      nodesLoading={nodesQuery.isLoading}
      onInstanceChange={setTargetInstanceId}
      onEnsureRun={onEnsureRun}
      onPreflight={onPreflight}
      onStart={onStart}
      onNotify={(level, message) => {
        if (level === 'success') toast.success(message)
        else toast.error(message)
      }}
      onFinished={(target) => {
        if (target.kind === 'session') navigate(`/bots/sessions/${target.runId}?tab=overview`)
        else navigate('/bots?tab=sessions')
      }}
    />
  )
}
