import { useEffect, useReducer, useState, type ComponentType } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { FieldError, FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import { WIZARD_STEPS, createDefaultDraft, draftTargetBots, isPlanTokenFresh, wizardReducer } from '@/lib/bot-load/bot-load-draft'
import type { BotLoadWizardDraft, WizardStep } from '@/lib/bot-load/bot-load-draft'
import type { BotLoadNodeCapacity, BotLoadPreflightResult, BotLoadTemplate } from '@/lib/bot-load/bot-load-types'
import { previewBotNames, validateCommandSchedule, validateConnection, validateLoadProfile, validateThresholds } from '@/lib/bot-load/bot-load-validation'
import { CapacityPlan } from '@/components/views/bot-load/CapacityPlan'
import { CommandPlanEditor } from '@/components/views/bot-load/CommandPlanEditor'
import { LoadProfileEditor } from '@/components/views/bot-load/LoadProfileEditor'
import { ThresholdEditor } from '@/components/views/bot-load/ThresholdEditor'

/** 实例选择器插槽所需的 props（应用侧组件，千级场景走服务端搜索）。 */
export interface InstancePickerSlotProps {
  value: number | null
  onChange: (id: number | null, inst?: { serverPort?: number } | null) => void
  enabled?: boolean
  placeholder?: string
}

export interface BotLoadWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 从模板启动时传入。 */
  template?: BotLoadTemplate | null
  /** 实例选择器（应用侧组件）。 */
  InstancePicker: ComponentType<InstancePickerSlotProps>
  /** 节点容量查询结果（容器经 useBotLoadNodes 取数）。 */
  nodes?: { items?: BotLoadNodeCapacity[]; totalCapacity?: number; availableCapacity?: number }
  nodesLoading?: boolean
  /** 目标实例变化（容器据此决定是否启用节点容量查询）。 */
  onInstanceChange?: (instanceId: number | null) => void
  /** 创建运行（容器注入 mutation）；返回 runId。 */
  onEnsureRun: (draft: BotLoadWizardDraft) => Promise<number>
  /** 预检（容器注入 mutation）。 */
  onPreflight: (args: { runId: number; executorNodeIds?: number[] }) => Promise<BotLoadPreflightResult>
  /** 启动（容器注入 mutation）。 */
  onStart: (args: { runId: number; planToken: string }) => Promise<{ id: number }>
  /** 结果提示（容器注入 toast）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
  /** 结束导航（容器注入路由）。 */
  onFinished?: (target: { kind: 'session'; runId: number } | { kind: 'list' }) => void
}

/**
 * 五步压测创建向导（FR-371）：目标 → 连接 → 命令编排 → 负载曲线 → 阈值预检。
 *
 * 草稿状态机（reducer）与全部校验留在包内；四个服务端交互（建运行 / 预检 / 启动 / 仅保存）
 * 由容器注入——失败时容器抛出携带服务端 message 的 Error，此处 catch 后就地展示，
 * 从而保住「服务端文案优先」的语义，同时包内不依赖 axios 的错误形状。
 *
 * planToken 不跨刷新持久化；字段变化立即作废。
 */
export function BotLoadWizard({
  open,
  onOpenChange,
  template,
  InstancePicker,
  nodes,
  nodesLoading,
  onInstanceChange,
  onEnsureRun,
  onPreflight,
  onStart,
  onNotify,
  onFinished,
}: BotLoadWizardProps) {
  const { t } = useTranslation()

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

  const targetBots = draftTargetBots(draft)
  const names = previewBotNames(draft.namePrefix, targetBots)
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

  /** 取容器抛出的服务端文案；无则回退到给定文案。 */
  const failureText = (e: unknown, fallback: string) =>
    e instanceof Error && e.message ? e.message : fallback

  /** 确保运行已创建（容器负责两条创建路径的分支），并记住 runId。 */
  const ensureRun = async (): Promise<number> => {
    if (draft.runId) return draft.runId
    const runId = await onEnsureRun(draft)
    dispatch({ type: 'patch', patch: { runId } })
    return runId
  }

  const runPreflight = async () => {
    setError('')
    setBusy(true)
    try {
      const runId = await ensureRun()
      const result = await onPreflight({
        runId,
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
        onNotify?.('success', t('botsLoad.preflightOk'))
      } else {
        dispatch({ type: 'invalidatePlan' })
        onNotify?.('error', t('botsLoad.preflightBlocked'))
      }
    } catch (e: unknown) {
      setError(failureText(e, t('botsLoad.preflightFailed')))
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
      const run = await onStart({ runId: draft.runId, planToken: draft.planToken })
      onNotify?.('success', t('botsLoad.startOk'))
      onOpenChange(false)
      onFinished?.({ kind: 'session', runId: run.id })
    } catch (e: unknown) {
      setError(failureText(e, t('botsLoad.startFailed')))
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
      onNotify?.('success', t('botsLoad.saveRunOk', { id: runId }))
      onOpenChange(false)
      onFinished?.({ kind: 'list' })
    } catch (e: unknown) {
      setError(failureText(e, t('botsLoad.saveRunFailed')))
    } finally {
      setBusy(false)
    }
  }

  const canStart = !!draft.planToken && isPlanTokenFresh(draft.planExpiresAt) && !!preflightResult?.ready && !busy

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
                  draft.step === s
                    ? 'border-primary bg-primary/10 font-semibold'
                    : 'text-muted-foreground hover:text-foreground',
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
                <InstancePicker
                  value={draft.instanceId}
                  onChange={(id, inst) => {
                    dispatch({ type: 'patch', patch: { instanceId: id } })
                    onInstanceChange?.(id)
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
                  enabled={open}
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
                  onValueChange={(v) => dispatch({ type: 'patch', patch: { executorMode: v as 'auto' | 'manual' } })}
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
                  nodes={nodes?.items}
                  totalCapacity={nodes?.totalCapacity}
                  availableCapacity={nodes?.availableCapacity}
                  selectedNodeIds={draft.executorNodeIds}
                  onToggleNode={toggleNode}
                  executorMode={draft.executorMode}
                  loading={nodesLoading}
                />
              )}
            </div>
          )}

          {draft.step === 'connection' && (
            <div className="space-y-3">
              <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
                <div className="space-y-1 md:col-span-2">
                  <FieldLabel required htmlFor="cfg-server">
                    {t('bots.serverAddr')}
                  </FieldLabel>
                  <Input
                    id="cfg-server"
                    value={draft.config.server}
                    onChange={(e) =>
                      dispatch({ type: 'patch', patch: { config: { ...draft.config, server: e.target.value } } })
                    }
                  />
                </div>
                <div className="space-y-1">
                  <FieldLabel required htmlFor="cfg-port">
                    {t('bots.port')}
                  </FieldLabel>
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
                        patch: { config: { ...draft.config, version: e.target.value || undefined } },
                      })
                    }
                    placeholder="1.20.1"
                  />
                </div>
              </div>
              <div className="space-y-1">
                <FieldLabel required htmlFor="cfg-prefix">
                  {t('bots.namePrefix')}
                </FieldLabel>
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
                nodes={nodes?.items ?? preflightResult?.nodeCapacities}
                totalCapacity={nodes?.totalCapacity}
                availableCapacity={preflightResult?.totalAvailable ?? nodes?.availableCapacity}
                preflight={preflightResult}
                selectedNodeIds={draft.executorNodeIds}
                onToggleNode={toggleNode}
                executorMode={draft.executorMode}
                loading={nodesLoading}
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
