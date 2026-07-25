import { useEffect, useMemo, useReducer, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import {
  useBotLoadNodes,
  useCreateBotLoadRun,
  useCreateBotLoadRunFromTemplate,
  usePreflightBotLoadRun,
  useStartBotLoadRun,
  type BotLoadPreflightResult,
  type BotLoadTemplate,
} from '@/api/botLoad'
import { useInstances } from '@/api/instances'
import {
  createDefaultDraft,
  draftTargetBots,
  isPlanTokenFresh,
  WIZARD_STEPS,
  wizardReducer,
  type WizardStep,
} from '@/lib/bot-load/draft'
import { previewBotNames, validateCommandSchedule, validateConnection, validateLoadProfile, validateThresholds } from '@/lib/bot-load/validation'
import CommandPlanEditor from './CommandPlanEditor'
import LoadProfileEditor from './LoadProfileEditor'
import ThresholdEditor from './ThresholdEditor'
import CapacityPlan from './CapacityPlan'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { cn } from '@jianmanager/ui'

interface BotLoadWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 从模板启动时传入 */
  template?: BotLoadTemplate | null
}

/**
 * 五步压测创建向导：目标 → 连接 → 命令编排 → 负载曲线 → 阈值预检。
 * planToken 不跨刷新持久化；字段变化立即作废。
 */
export default function BotLoadWizard({ open, onOpenChange, template }: BotLoadWizardProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: instances } = useInstances()
  const createRun = useCreateBotLoadRun()
  const createFromTemplate = useCreateBotLoadRunFromTemplate()
  const preflight = usePreflightBotLoadRun()
  const startRun = useStartBotLoadRun()

  const [draft, dispatch] = useReducer(
    wizardReducer,
    undefined,
    () =>
      createDefaultDraft(
        template
          ? {
              templateId: template.id,
              name: template.name,
              commandSchedule: structuredClone(template.commandSchedule),
              loadProfile: structuredClone(template.loadProfile),
              thresholds: structuredClone(template.thresholds),
            }
          : undefined,
      ),
  )
  const [preflightResult, setPreflightResult] = useState<BotLoadPreflightResult | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // 打开/换模板时重置草稿
  useEffect(() => {
    if (!open) return
    /* eslint-disable react-hooks/set-state-in-effect -- 弹窗打开/换模板时一次性重置，属合法同步 */
    dispatch({
      type: 'reset',
      draft: template
        ? {
            templateId: template.id,
            name: template.name,
            commandSchedule: structuredClone(template.commandSchedule),
            loadProfile: structuredClone(template.loadProfile),
            thresholds: structuredClone(template.thresholds),
          }
        : undefined,
    })
    setPreflightResult(null)
    setError('')
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [open, template])

  const nodesQuery = useBotLoadNodes(draft.instanceId, open && draft.instanceId !== null)
  const targetBots = draftTargetBots(draft)
  const names = previewBotNames(draft.namePrefix, targetBots)

  const instanceOptions: ComboboxOption[] = useMemo(
    () => (instances ?? []).map((i) => ({ value: String(i.id), label: i.name })),
    [instances],
  )

  const stepIndex = WIZARD_STEPS.indexOf(draft.step)

  const canNext = (): boolean => {
    if (draft.step === 'target') return draft.instanceId !== null && draft.instanceId > 0
    if (draft.step === 'connection') {
      return (
        validateConnection({
          server: draft.config.server,
          port: draft.config.port,
          auth: draft.config.auth,
          namePrefix: draft.namePrefix,
        }).length === 0
      )
    }
    if (draft.step === 'commands') return validateCommandSchedule(draft.commandSchedule).length === 0
    if (draft.step === 'profile') {
      return validateLoadProfile(draft.loadProfile).length === 0 && validateThresholds(draft.thresholds).length === 0
    }
    return true
  }

  const go = (step: WizardStep) => dispatch({ type: 'setStep', step })
  const next = () => {
    if (stepIndex < WIZARD_STEPS.length - 1) go(WIZARD_STEPS[stepIndex + 1])
  }
  const prev = () => {
    if (stepIndex > 0) go(WIZARD_STEPS[stepIndex - 1])
  }

  const toggleNode = (nodeId: number) => {
    const ids = draft.executorNodeIds.includes(nodeId)
      ? draft.executorNodeIds.filter((id) => id !== nodeId)
      : [...draft.executorNodeIds, nodeId]
    dispatch({ type: 'patch', patch: { executorNodeIds: ids, executorMode: 'manual' } })
  }

  const ensureRun = async (): Promise<number> => {
    if (draft.runId) return draft.runId
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
      dispatch({ type: 'patch', patch: { runId: run.id } })
      return run.id
    }
    const run = await createRun.mutateAsync({
      ...base,
      count: targetBots,
      commandSchedule: draft.commandSchedule,
      loadProfile: draft.loadProfile,
      thresholds: draft.thresholds,
    })
    dispatch({ type: 'patch', patch: { runId: run.id } })
    return run.id
  }

  const runPreflight = async () => {
    setError('')
    setBusy(true)
    try {
      const runId = await ensureRun()
      const result = await preflight.mutateAsync({
        id: runId,
        executorNodeIds: draft.executorMode === 'manual' ? draft.executorNodeIds : undefined,
      })
      setPreflightResult(result)
      if (result.ready && result.planToken && result.expiresAt) {
        dispatch({
          type: 'setPreflight',
          planToken: result.planToken,
          expiresAt: result.expiresAt,
          runId,
        })
        toast.success(t('botsLoad.preflightOk'))
      } else {
        dispatch({ type: 'invalidatePlan' })
        toast.error(t('botsLoad.preflightBlocked'))
      }
    } catch (e: unknown) {
      const msg =
        e && typeof e === 'object' && 'response' in e
          ? (e as { response?: { data?: { message?: string } } }).response?.data?.message
          : undefined
      setError(msg || t('botsLoad.preflightFailed'))
    } finally {
      setBusy(false)
    }
  }

  const start = async () => {
    if (!draft.runId || !draft.planToken || !isPlanTokenFresh(draft.planExpiresAt)) {
      setError(t('botsLoad.needFreshPreflight'))
      return
    }
    setBusy(true)
    setError('')
    try {
      const run = await startRun.mutateAsync({ id: draft.runId, planToken: draft.planToken })
      toast.success(t('botsLoad.startOk'))
      onOpenChange(false)
      navigate(`/bots/sessions/${run.id}?tab=overview`)
    } catch (e: unknown) {
      const msg =
        e && typeof e === 'object' && 'response' in e
          ? (e as { response?: { data?: { message?: string } } }).response?.data?.message
          : undefined
      setError(msg || t('botsLoad.startFailed'))
      dispatch({ type: 'invalidatePlan' })
      setPreflightResult(null)
    } finally {
      setBusy(false)
    }
  }

  const saveOnly = async () => {
    setBusy(true)
    setError('')
    try {
      const runId = await ensureRun()
      toast.success(t('botsLoad.saveRunOk', { id: runId }))
      onOpenChange(false)
      navigate('/bots?tab=sessions')
    } catch (e: unknown) {
      const msg =
        e && typeof e === 'object' && 'response' in e
          ? (e as { response?: { data?: { message?: string } } }).response?.data?.message
          : undefined
      setError(msg || t('botsLoad.saveRunFailed'))
    } finally {
      setBusy(false)
    }
  }

  const canStart =
    !!draft.planToken &&
    isPlanTokenFresh(draft.planExpiresAt) &&
    !!preflightResult?.ready &&
    !busy

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-3xl`}>
        <DialogHeader>
          <DialogTitle>{t('botsLoad.wizardTitle')}</DialogTitle>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-4 py-1">
          <nav aria-label={t('botsLoad.wizardSteps')} className="flex flex-wrap gap-1">
            {WIZARD_STEPS.map((s, i) => (
              <button
                key={s}
                type="button"
                className={cn(
                  'rounded-md border px-2.5 py-1 text-xs',
                  draft.step === s ? 'border-primary bg-primary/10 font-semibold' : 'text-muted-foreground',
                )}
                aria-current={draft.step === s ? 'step' : undefined}
                onClick={() => go(s)}
              >
                {i + 1}. {t(`botsLoad.step_${s}`)}
              </button>
            ))}
          </nav>

          {error && <div className="rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</div>}

          {draft.step === 'target' && (
            <div className="space-y-3">
              <div className="space-y-1">
                <FieldLabel required>{t('bots.instance')}</FieldLabel>
                <Combobox
                  options={instanceOptions}
                  value={draft.instanceId ? String(draft.instanceId) : ''}
                  onChange={(v) => {
                    const id = Number(v)
                    dispatch({ type: 'patch', patch: { instanceId: id || null } })
                    const inst = instances?.find((i) => i.id === id)
                    if (inst) {
                      dispatch({
                        type: 'patch',
                        patch: {
                          config: {
                            ...draft.config,
                            server: '127.0.0.1',
                            port: inst.serverPort && inst.serverPort > 0 ? inst.serverPort : 25565,
                          },
                        },
                      })
                    }
                  }}
                  allowCustom={false}
                  placeholder={t('bots.selectInstance')}
                />
              </div>
              <div className="space-y-1">
                <FieldLabel htmlFor="run-name">{t('botsLoad.runName')}</FieldLabel>
                <Input
                  id="run-name"
                  value={draft.name}
                  onChange={(e) => dispatch({ type: 'patch', patch: { name: e.target.value } })}
                />
              </div>
              <p className="text-sm text-muted-foreground">
                {t('botsLoad.targetBotsReadonly', { count: targetBots })}
              </p>
              <div className="space-y-1">
                <FieldLabel>{t('botsLoad.executorMode')}</FieldLabel>
                <Select
                  value={draft.executorMode}
                  onValueChange={(v) =>
                    dispatch({ type: 'patch', patch: { executorMode: v as 'auto' | 'manual' } })
                  }
                >
                  <SelectTrigger className="w-48">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="auto">{t('botsLoad.executorAuto')}</SelectItem>
                    <SelectItem value="manual">{t('botsLoad.executorManual')}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {draft.instanceId && (
                <CapacityPlan
                  nodes={nodesQuery.data?.items}
                  totalCapacity={nodesQuery.data?.totalCapacity}
                  availableCapacity={nodesQuery.data?.availableCapacity}
                  selectedNodeIds={draft.executorNodeIds}
                  onToggleNode={toggleNode}
                  executorMode={draft.executorMode}
                  loading={nodesQuery.isLoading}
                />
              )}
            </div>
          )}

          {draft.step === 'connection' && (
            <div className="space-y-3">
              <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
                <div className="space-y-1 md:col-span-2">
                  <FieldLabel required htmlFor="cfg-server">{t('bots.serverAddr')}</FieldLabel>
                  <Input
                    id="cfg-server"
                    value={draft.config.server}
                    onChange={(e) =>
                      dispatch({
                        type: 'patch',
                        patch: { config: { ...draft.config, server: e.target.value } },
                      })
                    }
                  />
                </div>
                <div className="space-y-1">
                  <FieldLabel required htmlFor="cfg-port">{t('bots.port')}</FieldLabel>
                  <Input
                    id="cfg-port"
                    type="number"
                    value={draft.config.port}
                    onChange={(e) =>
                      dispatch({
                        type: 'patch',
                        patch: { config: { ...draft.config, port: Number(e.target.value) } },
                      })
                    }
                  />
                </div>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1">
                  <FieldLabel>{t('bots.authMethod')}</FieldLabel>
                  <Input value={t('bots.offline')} readOnly disabled />
                  <p className="text-xs text-muted-foreground">{t('botsLoad.authOfflineOnly')}</p>
                </div>
                <div className="space-y-1">
                  <FieldLabel htmlFor="cfg-ver">{t('botsLoad.mcVersion')}</FieldLabel>
                  <Input
                    id="cfg-ver"
                    value={draft.config.version ?? ''}
                    onChange={(e) =>
                      dispatch({
                        type: 'patch',
                        patch: {
                          config: {
                            ...draft.config,
                            version: e.target.value || undefined,
                          },
                        },
                      })
                    }
                    placeholder="1.20.1"
                  />
                </div>
              </div>
              <div className="space-y-1">
                <FieldLabel required htmlFor="cfg-prefix">{t('bots.namePrefix')}</FieldLabel>
                <Input
                  id="cfg-prefix"
                  value={draft.namePrefix}
                  onChange={(e) => dispatch({ type: 'patch', patch: { namePrefix: e.target.value } })}
                />
                <FieldError
                  error={
                    validateConnection({
                      server: draft.config.server,
                      port: draft.config.port,
                      auth: draft.config.auth,
                      namePrefix: draft.namePrefix,
                    }).find((e) => e.path === 'namePrefix')?.message
                  }
                />
              </div>
              <p className="text-sm text-muted-foreground" aria-live="polite">
                {t('botsLoad.namePreview', { first: names.first, last: names.last })}
              </p>
            </div>
          )}

          {draft.step === 'commands' && (
            <CommandPlanEditor
              value={draft.commandSchedule}
              onChange={(schedule) => dispatch({ type: 'setCommandSchedule', schedule })}
            />
          )}

          {draft.step === 'profile' && (
            <div className="space-y-6">
              <LoadProfileEditor
                value={draft.loadProfile}
                onChange={(profile) => dispatch({ type: 'setLoadProfile', profile })}
              />
              <ThresholdEditor
                value={draft.thresholds}
                onChange={(thresholds) => dispatch({ type: 'setThresholds', thresholds })}
              />
            </div>
          )}

          {draft.step === 'preflight' && (
            <div className="space-y-4">
              <p className="text-sm text-muted-foreground">{t('botsLoad.preflightHint')}</p>
              <CapacityPlan
                nodes={nodesQuery.data?.items ?? preflightResult?.nodeCapacities}
                totalCapacity={nodesQuery.data?.totalCapacity}
                availableCapacity={preflightResult?.totalAvailable ?? nodesQuery.data?.availableCapacity}
                preflight={preflightResult}
                selectedNodeIds={draft.executorNodeIds}
                onToggleNode={toggleNode}
                executorMode={draft.executorMode}
                loading={nodesQuery.isLoading}
              />
              <div className="flex flex-wrap gap-2">
                <Button type="button" onClick={runPreflight} disabled={busy || !draft.instanceId}>
                  {busy ? t('common.loading') : t('botsLoad.runPreflight')}
                </Button>
                <Button type="button" variant="outline" onClick={saveOnly} disabled={busy || !draft.instanceId}>
                  {t('botsLoad.saveOnly')}
                </Button>
              </div>
            </div>
          )}
        </ScrollableDialogBody>
        <DialogFooter className="gap-2">
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          {stepIndex > 0 && (
            <Button type="button" variant="outline" onClick={prev} disabled={busy}>
              {t('botsLoad.prevStep')}
            </Button>
          )}
          {stepIndex < WIZARD_STEPS.length - 1 && (
            <Button type="button" onClick={next} disabled={!canNext() || busy}>
              {t('botsLoad.nextStep')}
            </Button>
          )}
          {draft.step === 'preflight' && (
            <Button type="button" onClick={start} disabled={!canStart}>
              {t('botsLoad.startRun')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
