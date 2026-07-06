import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useUpdateInstance } from '@/api/instances'
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
import { validateResourceLimitNumber } from '@/lib/form-validation'

interface EditInstanceLimitsDialogProps {
  instanceId: number
  instanceName: string
  /** 实例启动方式；仅 docker 模式资源限额生效（FR-079，ADR-019）。 */
  processType: string
  /** 当前 CPU 核数上限（0=不限制）。 */
  cpuLimit: number
  /** 当前内存上限（MiB，0=不限制）。 */
  memLimitMb: number
  /** 当前磁盘上限（MiB，0=不限制；v1 仅持久化展示）。 */
  diskLimitMb: number
  onClose: () => void
}

/** 0 视为「不限制」，编辑框留空展示；非 0 才回填具体值，避免把「不限制」显示成 0。 */
function toField(v: number): string {
  return v && v > 0 ? String(v) : ''
}

/**
 * 实例资源限额编辑器（FR-079）：docker 模式实例的 CPU 核数 / 内存 / 磁盘上限，
 * 留空/0/负值=不限制。变更经 PUT /instances/:id 持久化，对下一次启动生效。
 * 非 docker 模式不提供编辑，仅提示需切换到 docker 模式。
 */
export default function EditInstanceLimitsDialog({
  instanceId,
  instanceName,
  processType,
  cpuLimit,
  memLimitMb,
  diskLimitMb,
  onClose,
}: EditInstanceLimitsDialogProps) {
  const { t } = useTranslation()
  const update = useUpdateInstance()
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
    update.mutate(
      {
        id: instanceId,
        // 留空回落 0；0/负值均表示不限制（FR-079）。
        body: {
          cpuLimit: cpu.trim() ? Number(cpu) : 0,
          memLimitMb: mem.trim() ? Number(mem) : 0,
          diskLimitMb: disk.trim() ? Number(disk) : 0,
        },
      },
      {
        onSuccess: () => {
          toast.success(t('instances.resourceLimitSaved'))
          onClose()
        },
      },
    )
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
                disabled={update.isPending || hasError}
              >
                {update.isPending ? t('common.saving') : t('common.save')}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
