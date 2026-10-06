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
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type EditInstanceConfigNotice = (kind: 'success' | 'error', message: string) => void

/**
 * 实例配置编辑器（FR-233）：随时改启动命令 / 绑定 JDK / 自动重启，经 PUT /instances/:id 持久化、对下次启动生效。
 * 重绑 JDK 是「实例未绑定 JDK / Java 版本不符崩溃」的解药——给已建实例补绑合适大版本的 JDK。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast —— 可绑 JDK 选项由外壳按节点取好注入，
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

  const submit = async (e: FormEvent) => {
    e.preventDefault()
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
              <input
                value={cmd}
                onChange={(e) => setCmd(e.target.value)}
                className="mt-1 w-full rounded-md border bg-background px-3 py-2 font-mono text-sm"
                placeholder="java -Xmx2G -jar server.jar nogui"
              />
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
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={restart} onChange={(e) => setRestart(e.target.checked)} />
              {t('instanceDetail.autoRestart')}
            </label>
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button
              type="submit"
              disabled={saving}
            >
              {saving ? t('common.saving') : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
