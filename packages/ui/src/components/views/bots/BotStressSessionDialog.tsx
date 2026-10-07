import { useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { FieldError, FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import {
  hasErrors,
  validateFields,
  validateHost,
  validatePort,
  validateRequired,
} from '../../../lib/form-validation'
import { useFieldGate } from '../../../lib/use-field-gate'

/** 批量条与压测弹窗共用的行为集合（与后端 behavior 枚举对齐）。 */
const BEHAVIOR_OPTIONS = ['idle', 'guard', 'follow', 'patrol'] as const

/** 创建压测会话请求体（容器注入 mutation）。 */
export interface BotStressSessionBody {
  instanceId: number
  count: number
  behavior: string
  namePrefix: string
  config: { server: string; port: number; auth: string }
  orchestrationYaml?: string
}

/** 实例选择器插槽参数（应用侧 InstancePicker：千级实例须服务端搜索）。 */
export interface StressInstancePickerArgs {
  value: number | null
  /** 选中实例时一并回传其 serverPort，用于自动填充端口。 */
  onChange: (id: number | null, inst?: { serverPort?: number }) => void
  enabled: boolean
  placeholder: string
  invalid: boolean
}

export interface BotStressSessionDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 创建压测会话（容器注入 mutation）。 */
  onCreate: (body: BotStressSessionBody) => Promise<void>
  /** 提交中（禁用提交按钮）。 */
  submitting?: boolean
  /** 实例选择器插槽。 */
  renderInstancePicker: (args: StressInstancePickerArgs) => ReactNode
}
const DEFAULT_ORCHESTRATION_YAML = `loop: true
staggerMs: 500
phases:
  - durationSec: 60
    behavior: idle
  - durationSec: 120
    behavior: patrol
    target: "0,64,0;8,64,8"
  - durationSec: 60
    behavior: guard
  - durationSec: 90
    behavior: custom
    steps:
      - type: chat
        message: hello
      - type: wait
        durationMs: 3000
      - type: move
        pos:
          x: 0
          y: 64
          z: 0
`
export function BotStressSessionDialog({
  open,
  onOpenChange,
  onCreate,
  submitting = false,
  renderInstancePicker,
}: BotStressSessionDialogProps) {
  const { t } = useTranslation()
  const [instanceId, setInstanceId] = useState('')
  const [count, setCount] = useState('20')
  const [namePrefix, setNamePrefix] = useState('stress')
  const [server, setServer] = useState('')
  const [port, setPort] = useState('25565')
  const [auth, setAuth] = useState('offline')
  const [behavior, setBehavior] = useState('idle')
  const [orchestrationYaml, setOrchestrationYaml] = useState(DEFAULT_ORCHESTRATION_YAML)
  const [error, setError] = useState('')
  const gate = useFieldGate()

  // 千级实例只在弹窗打开时才展开：本组件是**常驻挂载**的（调用处写 `<XxxDialog open={...} />`
  // 而非 `{open && ...}`），而 Radix 的 DialogContent 关闭时虽不挂 DOM，其 children 仍会在每次
  // render 求值——不设门控时，每次渲染都会凭空创建 1200 个 ComboboxOption 对象。
  // 实例候选改由 InstancePicker 走服务端搜索，不再本地拉全量再映射。
  const parsedCount = Number(count)
  const errors = validateFields(
    { instanceId, count, namePrefix, server, port },
    {
      instanceId: [validateRequired],
      count: [validateRequired, (v) => (Number(v) >= 1 && Number(v) <= 5000 ? '' : t('bots.countRange'))],
      namePrefix: [validateRequired],
      server: [validateRequired, validateHost],
      port: [validateRequired, validatePort],
    },
  )

  const reset = () => {
    setInstanceId('')
    setCount('20')
    setNamePrefix('stress')
    setServer('')
    setPort('25565')
    setAuth('offline')
    setBehavior('idle')
    setOrchestrationYaml(DEFAULT_ORCHESTRATION_YAML)
    setError('')
    gate.reset()
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors)) return
    setError('')
    try {
      await onCreate({
        instanceId: Number(instanceId),
        count: parsedCount,
        behavior,
        namePrefix,
        config: { server, port: Number(port), auth },
        orchestrationYaml: orchestrationYaml.trim() ? orchestrationYaml : undefined,
      })
      onOpenChange(false)
      reset()
    } catch (err) {
      const msg =
        err instanceof Error && 'response' in err
          ? (err as { response?: { data?: { message?: string } } }).response?.data?.message
          : undefined
      setError(msg || t('bots.stressCreateFailed'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
        <DialogHeader>
          <DialogTitle>{t('bots.createStressSession')}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-1">
            {error && <div className="rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</div>}
            <div className="space-y-1">
              <FieldLabel required>{t('bots.instance')}</FieldLabel>
              {renderInstancePicker({
                value: instanceId ? Number(instanceId) : null,
                onChange: (id, inst) => {
                  gate.touch('instanceId')
                  setInstanceId(id === null ? '' : String(id))
                  if (inst) {
                    setServer('127.0.0.1')
                    setPort(String(inst.serverPort && inst.serverPort > 0 ? inst.serverPort : 25565))
                  }
                },
                enabled: open,
                placeholder: t('bots.selectInstance'),
                invalid: !!gate.show('instanceId', errors.instanceId),
              })}
              <FieldError error={gate.show('instanceId', errors.instanceId)} />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <FieldLabel required>{t('bots.namePrefix')}</FieldLabel>
                <Input aria-label={t('bots.namePrefix')} value={namePrefix} onChange={(e) => setNamePrefix(e.target.value)} onBlur={() => gate.touch('namePrefix')} />
                <FieldError error={gate.show('namePrefix', errors.namePrefix)} />
              </div>
              <div className="space-y-1">
                <FieldLabel required>{t('bots.count')}</FieldLabel>
                <Input aria-label={t('bots.count')} value={count} type="number" onChange={(e) => setCount(e.target.value)} onBlur={() => gate.touch('count')} />
                <FieldError error={gate.show('count', errors.count)} />
              </div>
            </div>
            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2 space-y-1">
                <FieldLabel required>{t('bots.serverAddr')}</FieldLabel>
                <Input aria-label={t('bots.serverAddr')} value={server} onChange={(e) => setServer(e.target.value)} onBlur={() => gate.touch('server')} aria-invalid={!!gate.show('server', errors.server)} />
                <FieldError error={gate.show('server', errors.server)} />
              </div>
              <div className="space-y-1">
                <FieldLabel required>{t('bots.port')}</FieldLabel>
                <Input aria-label={t('bots.port')} value={port} onChange={(e) => setPort(e.target.value)} type="number" onBlur={() => gate.touch('port')} aria-invalid={!!gate.show('port', errors.port)} />
                <FieldError error={gate.show('port', errors.port)} />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <FieldLabel>{t('bots.authMethod')}</FieldLabel>
                <Select value={auth} onValueChange={setAuth}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="offline">{t('bots.offline')}</SelectItem>
                    <SelectItem value="microsoft">{t('bots.microsoft')}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1">
                <FieldLabel>{t('bots.initialBehavior')}</FieldLabel>
                <Select value={behavior} onValueChange={setBehavior}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {BEHAVIOR_OPTIONS.map((b) => (
                      <SelectItem key={b} value={b}>{t(`bots.${b}`)}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between gap-3">
                <FieldLabel>{t('bots.orchestrationYaml')}</FieldLabel>
                <Button
                  type="button"
                  size="xs"
                  variant="outline"
                  onClick={() => setOrchestrationYaml(DEFAULT_ORCHESTRATION_YAML)}
                >
                  {t('bots.restoreTemplate')}
                </Button>
              </div>
              <textarea
                aria-label={t('bots.orchestrationYaml')}
                className="min-h-72 w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 font-mono text-sm leading-5 shadow-xs outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40"
                value={orchestrationYaml}
                onChange={(e) => setOrchestrationYaml(e.target.value)}
                spellCheck={false}
              />
            </div>
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                onOpenChange(false)
                reset()
              }}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={submitting || hasErrors(errors)}>
              {submitting ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
