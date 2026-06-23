import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useUpdateInstance } from '@/api/instances'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateNonNegativeNumber } from '@/lib/form-validation'

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
 * 留空=不限制（保存为 0）。变更经 PUT /instances/:id 持久化，对下一次启动生效。
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

  const cpuErr = validateNonNegativeNumber(cpu)
  const memErr = validateNonNegativeNumber(mem)
  const diskErr = validateNonNegativeNumber(disk)
  const hasError = !!cpuErr || !!memErr || !!diskErr

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (hasError) return
    update.mutate(
      {
        id: instanceId,
        // 留空回落 0=清除限制（FR-079）。
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
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-md`}>
        <h2 className="text-lg font-bold mb-4">{t('instances.resourceLimitTitle', { name: instanceName })}</h2>

        {!isDocker ? (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">{t('instances.resourceLimitDockerOnly')}</p>
            <div className="flex justify-end">
              <button type="button" onClick={onClose} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
                {t('common.close')}
              </button>
            </div>
          </div>
        ) : (
          <form onSubmit={submit} className="space-y-4">
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

            <div className="flex justify-end gap-2 pt-2">
              <button type="button" onClick={onClose} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
                {t('common.cancel')}
              </button>
              <button
                type="submit"
                disabled={update.isPending || hasError}
                className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
              >
                {update.isPending ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  )
}
