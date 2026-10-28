import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Switch } from '@jianmanager/ui/components/switch'

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type EditInstanceConfigNotice = (kind: 'success' | 'error', message: string) => void

/**
 * 实例配置编辑器（FR-233）：随时改启动命令 / 绑定 JDK / 自动重启，经 PUT /instances/:id 持久化、对下次启动生效。
 * 重绑 JDK 是「实例未绑定 JDK / Java 版本不符崩溃」的解药——给已建实例补绑合适大版本的 JDK。
 *
 * 受控视图（ADR-102 b 范式）：不取数、不发请求、不弹 toast —— 可绑 JDK 选项由外壳按节点取好注入，
 * 保存经 `onSave` 上报。本地编辑态、「留空=解绑」的语义、成功才关窗留在视图内。
 */
export interface EditInstanceConfigDialogProps {
  instanceName: string
  startCommand: string
  /** 当前绑定的 JDK id（0=未绑定/系统默认）。 */
  jdkId: number
  autoRestart: boolean
  onClose: () => void
  /** 可绑定的 JDK 选项（外壳按实例所属节点取）。 */
  jdkOptions: ComboboxOption[]
  /** 保存。失败请抛错，视图取服务端 message 提示；成功后视图自行关窗。 */
  onSave: (payload: { startCommand: string; jdkId: number; autoRestart: boolean }) => Promise<void>
  /** 提示通道。 */
  notify: EditInstanceConfigNotice
}

export default function EditInstanceConfigDialog({
  instanceName,
  startCommand,
  jdkId,
  autoRestart,
  onClose,
  jdkOptions,
  onSave,
  notify,
}: EditInstanceConfigDialogProps) {
  const { t } = useTranslation()

  const [cmd, setCmd] = useState(startCommand)
  const [jdk, setJdk] = useState(jdkId ? String(jdkId) : '')
  const [restart, setRestart] = useState(autoRestart)
  const [saving, setSaving] = useState(false)

  /*
    改进①「未修改保护」：什么都没改时保存按钮禁用。
    此前无论是否改动都能点保存，会发一次与当前值完全相同的 PUT——既浪费一次写请求，
    也会在审计里留下一条无意义的变更记录（排查「谁在改配置」时是噪音）。
    jdk 用字符串态（''=未绑定），比较时对回数字，避免 '0' 与 '' 的假差异。
  */
  const jdkNumber = jdk ? Number(jdk) : 0
  const dirty = cmd !== startCommand || jdkNumber !== jdkId || restart !== autoRestart

  /*
    改进②「启动命令必填校验」：留空会让实例再也起不来，而此前可以静默保存一个空命令。
    这里做最小非空校验（不猜命令格式：不同核心/包装器写法差异太大，误判比不判更有害）。
  */
  const cmdEmpty = cmd.trim() === ''
  const canSubmit = dirty && !cmdEmpty && !saving

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    setSaving(true)
    try {
      await onSave({
        startCommand: cmd,
        // jdk 留空=解绑（系统默认）；选中=绑定该 JDK。变更对下一次启动生效。
        jdkId: jdk ? Number(jdk) : 0,
        autoRestart: restart,
      })
      notify('success', t('instances.configSaved'))
      onClose()
    } catch (err) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg || t('instances.configSaveFailed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader>
          <DialogTitle>{t('instances.editConfigTitle', { name: instanceName })}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-4">
            <div>
              <FieldLabel>{t('instanceDetail.startCommand')}</FieldLabel>
              {/*
                改进③：原来是裸 <input>，与对话框里其它字段（Combobox/Switch）的边框、圆角、
                聚焦环都不一致；改用共享 Input 组件后视觉统一，也自动继承主题与暗色模式。
                同时补 aria-invalid + 错误提示，不留「为什么保存按钮点不动」的疑问。
              */}
              <Input
                value={cmd}
                onChange={(e) => setCmd(e.target.value)}
                className="mt-1 w-full font-mono text-sm"
                placeholder="java -Xmx2G -jar server.jar nogui"
                aria-invalid={cmdEmpty || undefined}
                aria-describedby={cmdEmpty ? 'edit-instance-cmd-error' : undefined}
              />
              {cmdEmpty && (
                <p id="edit-instance-cmd-error" className="mt-1 text-xs text-status-danger">
                  {t('instances.startCommandRequired', '启动命令不能为空，否则实例无法启动')}
                </p>
              )}
            </div>
            <div>
              <FieldLabel>{t('instances.jdkBinding')}</FieldLabel>
              <div className="mt-1">
                <Combobox
                  options={jdkOptions}
                  value={jdk}
                  onChange={setJdk}
                  allowCustom={false}
                  placeholder={t('instances.jdkSystemDefault')}
                />
              </div>
              <p className="mt-1 text-xs text-muted-foreground">{t('instances.jdkBindingHint')}</p>
            </div>
            <div className="flex items-center gap-2 text-sm">
              {/* 开关语义（启用/禁用）用 Switch；标签用 id + aria-labelledby 关联，
                  不再依赖 label 包裹——Radix Switch 渲染的是 button，包在 label 里点文字不会翻转。 */}
              <Switch id="edit-instance-auto-restart" checked={restart} onCheckedChange={setRestart} />
              <label htmlFor="edit-instance-auto-restart" className="cursor-pointer">
                {t('instanceDetail.autoRestart')}
              </label>
            </div>
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {saving ? t('common.saving') : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
