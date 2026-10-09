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
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateResourceLimitNumber } from '@/lib/shared/form-validation'

/** 0 视为「不限制」，编辑框留空展示；非 0 才回填具体值，避免把「不限制」显示成 0。 */
function toField(v: number): string {
  return v && v > 0 ? String(v) : ''
}

/**
 * 实例资源限额编辑器（FR-079）：docker 模式实例的 CPU 核数 / 内存 / 磁盘上限，
 * 留空/0/负值=不限制。变更由外壳持久化，对下一次启动生效。
 * 非 docker 模式不提供编辑，仅提示需切换到 docker 模式。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast——当前限额经 props 注入，
 * 保存经 `onSave` 上报（**留空回落 0** 的语义留在视图内，它是编辑器的输入约定）。
 * 「留空/0/负值均表示不限制」的校验用包内纯函数，与创建实例表单同源。
 */
export interface EditInstanceLimitsDialogProps {
  /** 实例展示名（标题用）。 */
  instanceName: string
  /** 实例启动方式；仅 docker 模式资源限额生效（FR-079，ADR-019）。 */
  processType: string
  /** 当前 CPU 核数上限（0=不限制）。 */
  cpuLimit: number
  /** 当前内存上限（MiB，0=不限制）。 */
  memLimitMb: number
  /** 当前磁盘上限（MiB，0=不限制；v1 仅持久化展示）。 */
  diskLimitMb: number
  /** 保存在途。 */
  saving?: boolean
  /** 关闭。 */
  onClose: () => void
  /** 保存限额。返回是否成功（成功则由视图关闭）。 */
  onSave: (limits: { cpuLimit: number; memLimitMb: number; diskLimitMb: number }) => Promise<boolean>
}

export default function EditInstanceLimitsDialog({
  instanceName,
  processType,
  cpuLimit,
  memLimitMb,
  diskLimitMb,
  saving = false,
  onClose,
  onSave,
}: EditInstanceLimitsDialogProps) {
  const { t } = useTranslation()
  const isDocker = processType === 'docker'

  const [cpu, setCpu] = useState(toField(cpuLimit))
  const [mem, setMem] = useState(toField(memLimitMb))
  const [disk, setDisk] = useState(toField(diskLimitMb))

  const cpuErr = validateResourceLimitNumber(cpu)
  const memErr = validateResourceLimitNumber(mem)
  const diskErr = validateResourceLimitNumber(disk)
  const hasError = !!cpuErr || !!memErr || !!diskErr

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (hasError) return
    void onSave({
      // 留空回落 0；0/负值均表示不限制（FR-079）。
      cpuLimit: cpu.trim() ? Number(cpu) : 0,
      memLimitMb: mem.trim() ? Number(mem) : 0,
      diskLimitMb: disk.trim() ? Number(disk) : 0,
    }).then((ok) => {
      if (ok) onClose()
    })
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader>
          <DialogTitle>{t('instances.resourceLimitTitle', { name: instanceName })}</DialogTitle>
        </DialogHeader>

        {!isDocker ? (
          <>
            <ScrollableDialogBody>
              <p className="text-sm text-muted-foreground">{t('instances.resourceLimitDockerOnly')}</p>
            </ScrollableDialogBody>
            <DialogFooter className="pt-4">
              <Button type="button" variant="outline" onClick={onClose}>
                {t('common.close')}
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
            <ScrollableDialogBody className="space-y-4">
              <div>
                <FieldLabel>{t('instances.cpuLimit')}</FieldLabel>
                <input
                  value={cpu}
                  onChange={(e) => setCpu(e.target.value)}
                  className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                  placeholder="1.5"
                  inputMode="decimal"
                  aria-invalid={!!cpuErr}
                />
                {cpuErr ? <FieldError error={cpuErr} /> : (
                  <p className="mt-1 text-xs text-muted-foreground">{t('instances.resourceLimitHint')}</p>
                )}
              </div>
              <div>
                <FieldLabel>{t('instances.memLimit')}</FieldLabel>
                <input
                  value={mem}
                  onChange={(e) => setMem(e.target.value)}
                  className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                  placeholder="2048"
                  inputMode="numeric"
                  aria-invalid={!!memErr}
                />
                {memErr ? <FieldError error={memErr} /> : (
                  <p className="mt-1 text-xs text-muted-foreground">{t('instances.resourceLimitHint')}</p>
                )}
              </div>
              <div>
                <FieldLabel>{t('instances.diskLimit')}</FieldLabel>
                <input
                  value={disk}
                  onChange={(e) => setDisk(e.target.value)}
                  className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                  placeholder="10240"
                  inputMode="numeric"
                  aria-invalid={!!diskErr}
                />
                {diskErr ? <FieldError error={diskErr} /> : (
                  <p className="mt-1 text-xs text-muted-foreground">{t('instances.diskLimitHint')}</p>
                )}
              </div>
            </ScrollableDialogBody>

            <DialogFooter className="pt-4">
              <Button type="button" variant="outline" onClick={onClose}>
                {t('common.cancel')}
              </Button>
              <Button
                type="submit"
                disabled={saving || hasError}
              >
                {saving ? t('common.saving') : t('common.save')}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
