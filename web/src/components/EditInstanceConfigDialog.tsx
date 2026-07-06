import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useUpdateInstance } from '@/api/instances'
import { useNodeJDKs } from '@/api/jdks'
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

interface EditInstanceConfigDialogProps {
  instanceId: number
  instanceName: string
  /** 实例所属节点 id，用于列出该节点已登记的 JDK 供绑定。 */
  nodeId: number
  /** 当前绑定的 JDK id（0=未绑定/系统默认）。 */
  jdkId: number
  startCommand: string
  autoRestart: boolean
  onClose: () => void
}

/**
 * 实例配置编辑器（FR-233）：随时改启动命令 / 绑定 JDK / 自动重启，经 PUT /instances/:id 持久化、对下次启动生效。
 * 重绑 JDK 是「实例未绑定 JDK / Java 版本不符崩溃」的解药——给已建实例补绑合适大版本的 JDK。
 */
export default function EditInstanceConfigDialog({
  instanceId,
  instanceName,
  nodeId,
  jdkId,
  startCommand,
  autoRestart,
  onClose,
}: EditInstanceConfigDialogProps) {
  const { t } = useTranslation()
  const update = useUpdateInstance()
  const { data: jdks } = useNodeJDKs(nodeId)

  const [cmd, setCmd] = useState(startCommand)
  const [jdk, setJdk] = useState(jdkId ? String(jdkId) : '')
  const [restart, setRestart] = useState(autoRestart)

  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))

  const submit = (e: FormEvent) => {
    e.preventDefault()
    update.mutate(
      {
        id: instanceId,
        // jdk 留空=解绑（系统默认）；选中=绑定该 JDK。变更对下一次启动生效。
        body: { startCommand: cmd, jdkId: jdk ? Number(jdk) : 0, autoRestart: restart },
      },
      {
        onSuccess: () => {
          toast.success(t('instances.configSaved'))
          onClose()
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          toast.error(err.response?.data?.message || t('instances.configSaveFailed'))
        },
      },
    )
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
              disabled={update.isPending}
            >
              {update.isPending ? t('common.saving') : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
