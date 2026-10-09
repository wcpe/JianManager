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
import { Input } from '@jianmanager/ui/components/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateRequired, validateHost, validatePort, validateFields, hasErrors } from '@/lib/shared/form-validation'

/** 创建结果：成功；或「创建成功但委托 Worker 失败」（需留在弹窗内显示可操作原因）。 */
export type CreateBotOutcome = { ok: true } | { ok: false; error: string }

/**
 * 控制台 Bot 段「新建 Bot」对话框（FR-039）。
 * 实例 id 由当前工作区实例预填（不可改），连接地址用「所在节点 host + 默认端口」预填且可改。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求——归属实例名与建议连接地址由外壳算好注入
 * （它们要查实例与节点），创建经 `onCreate` 上报。
 *
 * 创建结果的**两段语义**留在视图内：真机上出现过「Bot 已创建、但委托 Worker 失败」，
 * 若一律关窗就会留下一个永远 pending 的 Bot（两侧零反馈）。故 `onCreate` 用 `ok:false`
 * 区分这种情形，视图据此留在弹窗内显示原因而非静默关闭。
 */
/** 实例选择器插槽参数（无预设实例的场景，如全局 Bot 管理页）。 */
export interface CreateBotInstancePickerArgs {
  value: number | null
  /** 选中实例时一并回传其 serverPort，用于自动填充端口。 */
  onChange: (id: number | null, inst?: { serverPort?: number }) => void
}

export interface CreateBotDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 归属实例的展示名（只读展示）；提供 `renderInstancePicker` 时忽略。 */
  instanceName?: string
  /** 建议的连接地址（外壳按节点 host 与实例端口算出）。 */
  suggestedServer: string
  /** 建议的端口。 */
  suggestedPort: number
  /** 创建在途。 */
  creating?: boolean
  /** 创建 Bot。 */
  onCreate: (payload: {
    name: string
    server: string
    port: number
    auth: string
    behavior: string
  }) => Promise<CreateBotOutcome>
  /**
   * 实例选择器插槽：提供时替代只读实例名（全局 Bot 管理页需先选实例）。
   * 千级实例须走服务端搜索，故由外壳注入。
   */
  renderInstancePicker?: (args: CreateBotInstancePickerArgs) => ReactNode
  /** 插槽场景下实例是否已选（参与必填校验）。 */
  instanceSelected?: boolean
  /** 插槽场景下的当前实例 id（受控，由外壳持有）。 */
  instanceId?: number | null
  /** 插槽选中实例时回调（外壳据此更新实例 id 与建议连接地址）。 */
  onInstancePick?: (id: number | null, inst?: { serverPort?: number }) => void
}

export default function CreateBotDialog({
  open,
  onOpenChange,
  instanceName,
  suggestedServer,
  suggestedPort,
  creating = false,
  onCreate,
  renderInstancePicker,
  instanceSelected = true,
  instanceId = null,
  onInstancePick,
}: CreateBotDialogProps) {
  const { t } = useTranslation()

  const [name, setName] = useState('')
  const [auth, setAuth] = useState('offline')
  const [behavior, setBehavior] = useState('idle')
  const [error, setError] = useState('')
  // server/port 用户覆盖值：null 表示未改，跟随建议值显示（避免在 effect 里同步 props）
  const [serverOverride, setServerOverride] = useState<string | null>(null)
  const [portOverride, setPortOverride] = useState<string | null>(null)

  const server = serverOverride ?? suggestedServer
  const port = portOverride ?? String(suggestedPort)

  const errors = validateFields(
    { name, server, port },
    {
      name: [validateRequired],
      server: [validateRequired, validateHost],
      port: [validateRequired, validatePort],
    },
  )

  const behaviorOptions = [
    { value: 'idle', label: t('bots.idle') },
    { value: 'guard', label: t('bots.guard') },
    { value: 'follow', label: t('bots.follow') },
    { value: 'patrol', label: t('bots.patrol') },
  ]

  const resetForm = () => {
    setName('')
    setAuth('offline')
    setBehavior('idle')
    setError('')
    setServerOverride(null)
    setPortOverride(null)
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (hasErrors(errors)) return
    setError('')
    const outcome = await onCreate({ name, server, port: Number(port), auth, behavior })
    if (!outcome.ok) {
      // 创建成功但委托失败：留在弹窗内显示可操作原因，不静默关窗。
      setError(outcome.error)
      return
    }
    onOpenChange(false)
    resetForm()
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader>
          <DialogTitle>{t('bots.createBot')}</DialogTitle>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-1">
            {error && (
              <div className="p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
            )}

            <div className="space-y-1">
              <FieldLabel required={!!renderInstancePicker}>{t('bots.instance')}</FieldLabel>
              {renderInstancePicker ? (
                renderInstancePicker({ value: instanceId, onChange: (id, inst) => onInstancePick?.(id, inst) })
              ) : (
                <Input value={instanceName ?? ''} disabled readOnly />
              )}
              {renderInstancePicker && !instanceSelected && (
                <FieldError error={t('validation.required')} />
              )}
            </div>

            <div className="space-y-1">
              <FieldLabel required>{t('bots.name')}</FieldLabel>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="GuardBot"
                aria-invalid={!!errors.name}
              />
              <FieldError error={errors.name} />
            </div>

            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2 space-y-1">
                <FieldLabel required>{t('bots.serverAddr')}</FieldLabel>
                <Input
                  value={server}
                  onChange={(e) => setServerOverride(e.target.value)}
                  placeholder="mc.example.com"
                  aria-invalid={!!errors.server}
                />
                <FieldError error={errors.server} />
              </div>
              <div className="space-y-1">
                <FieldLabel required>{t('bots.port')}</FieldLabel>
                <Input
                  value={port}
                  onChange={(e) => setPortOverride(e.target.value)}
                  type="number"
                  aria-invalid={!!errors.port}
                />
                <FieldError error={errors.port} />
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
                    {behaviorOptions.map((opt) => (
                      <SelectItem key={opt.value} value={opt.value}>
                        {opt.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
          </ScrollableDialogBody>

          <DialogFooter className="pt-4">
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                onOpenChange(false)
                resetForm()
              }}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={creating || hasErrors(errors)}>
              {creating ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
