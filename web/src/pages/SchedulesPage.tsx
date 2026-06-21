import { Fragment, useEffect, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useSchedules,
  useCreateSchedule,
  useUpdateSchedule,
  useDeleteSchedule,
  useScheduleLogs,
  type ScheduleInfo,
} from '@/api/schedules'
import { useInstances } from '@/api/instances'
import { validateCron } from '@/lib/cron'
import {
  SCHEDULE_ACTIONS,
  EMPTY_SCHEDULE_FORM,
  formFromSchedule,
  toCreateBody,
  toUpdateBody,
  type ScheduleFormState,
} from '@/pages/schedule-form'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Panel } from '@/components/ui/panel'
import { StatusBadge } from '@/components/ui/status-badge'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel } from '@/components/ui/field-label'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import DangerConfirm from '@/components/DangerConfirm'

export default function SchedulesPage() {
  const { t } = useTranslation()
  const { data: schedules, isLoading } = useSchedules()
  const { data: instances } = useInstances()

  const createSchedule = useCreateSchedule()
  const updateSchedule = useUpdateSchedule()
  const deleteSchedule = useDeleteSchedule()

  const [showCreate, setShowCreate] = useState(false)
  // 正在编辑的任务（null 表示未编辑）。
  const [editing, setEditing] = useState<ScheduleInfo | null>(null)
  // 待删除确认的任务。
  const [deleteTarget, setDeleteTarget] = useState<ScheduleInfo | null>(null)
  // 展开查看日志的任务 ID。
  const [logsId, setLogsId] = useState<number | null>(null)

  const instanceName = (id: number) =>
    instances?.find((i) => i.id === id)?.name ?? `#${id}`

  const handleToggleEnabled = (s: ScheduleInfo) => {
    updateSchedule.mutate(
      { id: s.id, body: { enabled: !s.enabled } },
      {
        onSuccess: () =>
          toast.success(s.enabled ? t('schedules.disabledToast') : t('schedules.enabledToast')),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      },
    )
  }

  const confirmDelete = () => {
    if (!deleteTarget) return
    const target = deleteTarget
    setDeleteTarget(null)
    if (logsId === target.id) setLogsId(null)
    deleteSchedule.mutate(target.id, {
      onSuccess: () => toast.success(t('schedules.deletedToast')),
      onError: (e: Error & { response?: { data?: { message?: string } } }) =>
        toast.error(e?.response?.data?.message || t('common.error')),
    })
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">{t('schedules.title')}</h1>
        <Button onClick={() => setShowCreate(true)}>+ {t('schedules.createSchedule')}</Button>
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <Panel bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('schedules.name')}</TableHead>
                <TableHead>{t('schedules.instance')}</TableHead>
                <TableHead>{t('schedules.cron')}</TableHead>
                <TableHead>{t('schedules.action')}</TableHead>
                <TableHead>{t('schedules.enabled')}</TableHead>
                <TableHead>{t('schedules.lastRun')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(schedules ?? []).map((s) => {
                const expanded = logsId === s.id
                return (
                  <Fragment key={s.id}>
                    <TableRow>
                      <TableCell className="font-medium">{s.name}</TableCell>
                      <TableCell className="text-muted-foreground">{instanceName(s.instanceId)}</TableCell>
                      <TableCell className="font-mono text-xs">{s.cronExpr}</TableCell>
                      <TableCell>{t(`schedules.action_${s.action}`, { defaultValue: s.action })}</TableCell>
                      <TableCell>
                        <StatusBadge
                          level={s.enabled ? 'success' : 'neutral'}
                          label={s.enabled ? t('common.enabled') : t('common.disabled')}
                        />
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {s.lastRun ? new Date(s.lastRun).toLocaleString() : t('schedules.neverRun')}
                      </TableCell>
                      <TableCell className="space-x-3 text-right whitespace-nowrap">
                        <button
                          className="text-xs text-primary hover:underline"
                          onClick={() => setLogsId(expanded ? null : s.id)}
                        >
                          {expanded ? t('schedules.hideLogs') : t('schedules.viewLogs')}
                        </button>
                        <button
                          className="text-xs text-status-warning hover:underline"
                          onClick={() => handleToggleEnabled(s)}
                        >
                          {s.enabled ? t('schedules.disable') : t('schedules.enable')}
                        </button>
                        <button
                          className="text-xs text-primary hover:underline"
                          onClick={() => setEditing(s)}
                        >
                          {t('common.edit')}
                        </button>
                        <button
                          className="text-xs text-status-danger hover:underline"
                          onClick={() => setDeleteTarget(s)}
                        >
                          {t('common.delete')}
                        </button>
                      </TableCell>
                    </TableRow>
                    {expanded && (
                      <TableRow>
                        <TableCell colSpan={7} className="p-0">
                          <ScheduleLogs scheduleId={s.id} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                )
              })}
              {(!schedules || schedules.length === 0) && (
                <TableRow>
                  <TableCell colSpan={7} className="text-center text-muted-foreground">
                    {t('schedules.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </Panel>
      )}

      {/* 创建对话框 */}
      <ScheduleFormDialog
        open={showCreate}
        mode="create"
        instances={instances ?? []}
        submitting={createSchedule.isPending}
        onClose={() => setShowCreate(false)}
        onSubmit={(form) => {
          createSchedule.mutate(toCreateBody(form), {
            onSuccess: () => {
              toast.success(t('schedules.createdToast'))
              setShowCreate(false)
            },
            onError: (e: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(e?.response?.data?.message || t('schedules.createFailed')),
          })
        }}
      />

      {/* 编辑对话框：后端仅接收 cron/action/enabled。 */}
      <ScheduleFormDialog
        open={editing !== null}
        mode="edit"
        initial={editing}
        instances={instances ?? []}
        submitting={updateSchedule.isPending}
        onClose={() => setEditing(null)}
        onSubmit={(form) => {
          if (!editing) return
          updateSchedule.mutate(
            { id: editing.id, body: toUpdateBody(form) },
            {
              onSuccess: () => {
                toast.success(t('schedules.updatedToast'))
                setEditing(null)
              },
              onError: (e: Error & { response?: { data?: { message?: string } } }) =>
                toast.error(e?.response?.data?.message || t('common.error')),
            },
          )
        }}
      />

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('schedules.deleteTitle', { name: deleteTarget?.name ?? '' })}
        description={t('schedules.deleteDesc')}
        confirmLabel={t('common.delete')}
        scope="group"
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/** 执行日志展开区：调 useScheduleLogs 列出时间/结果/输出（FR-012）。 */
function ScheduleLogs({ scheduleId }: { scheduleId: number }) {
  const { t } = useTranslation()
  const { data, isLoading } = useScheduleLogs(scheduleId)

  return (
    <div className="bg-muted/30 p-3">
      {isLoading ? (
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      ) : !data || data.items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('schedules.noLogs')}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('schedules.logTime')}</TableHead>
              <TableHead>{t('schedules.logAction')}</TableHead>
              <TableHead>{t('schedules.logResult')}</TableHead>
              <TableHead>{t('schedules.logOutput')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.items.map((log) => (
              <TableRow key={log.id}>
                <TableCell className="text-muted-foreground whitespace-nowrap">
                  {new Date(log.startedAt).toLocaleString()}
                </TableCell>
                <TableCell>{t(`schedules.action_${log.action}`, { defaultValue: log.action })}</TableCell>
                <TableCell>
                  <StatusBadge
                    level={log.status === 'success' ? 'success' : 'danger'}
                    label={log.status === 'success' ? t('schedules.logSuccess') : t('schedules.logFailed')}
                  />
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  {log.error || '--'}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

interface ScheduleFormDialogProps {
  open: boolean
  /** create：可选实例/名称；edit：实例/名称只读，仅改 cron/action/启用。 */
  mode: 'create' | 'edit'
  initial?: ScheduleInfo | null
  instances: { id: number; name: string }[]
  submitting: boolean
  onClose: () => void
  onSubmit: (form: ScheduleFormState) => void
}

/** 创建/编辑定时任务对话框（含 Cron 基本校验与 command 条件输入）。 */
function ScheduleFormDialog({
  open,
  mode,
  initial,
  instances,
  submitting,
  onClose,
  onSubmit,
}: ScheduleFormDialogProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<ScheduleFormState>(EMPTY_SCHEDULE_FORM)

  // 打开时按模式初始化表单：编辑回填，创建清空。
  useEffect(() => {
    if (!open) return
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 弹窗打开瞬间一次性回填/清空表单，非渲染期联动
    setForm(mode === 'edit' && initial ? formFromSchedule(initial) : EMPTY_SCHEDULE_FORM)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 仅在弹窗打开瞬间初始化一次
  }, [open])

  const cron = useMemo(() => validateCron(form.cronExpr), [form.cronExpr])
  const instanceMissing = mode === 'create' && form.instanceId === ''
  const nameMissing = mode === 'create' && form.name.trim() === ''
  const canSubmit = !submitting && cron.valid && !instanceMissing && !nameMissing

  const instanceOptions: ComboboxOption[] = instances.map((i) => ({ value: String(i.id), label: i.name }))
  const actionOptions: ComboboxOption[] = SCHEDULE_ACTIONS.map((a) => ({ value: a, label: t(`schedules.action_${a}`) }))

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    onSubmit(form)
  }

  return (
    <Dialog open={open} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>
            {mode === 'create' ? t('schedules.createSchedule') : t('schedules.editSchedule')}
          </DialogTitle>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-1">
            <div className="space-y-1.5">
              <FieldLabel required={mode === 'create'}>{t('schedules.instance')}</FieldLabel>
              {mode === 'create' ? (
                <Combobox
                  options={instanceOptions}
                  value={form.instanceId}
                  onChange={(v) => setForm({ ...form, instanceId: v })}
                  allowCustom={false}
                  placeholder={t('schedules.selectInstance')}
                  invalid={instanceMissing}
                />
              ) : (
                <div className="w-full rounded-md border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
                  {initial ? (instances.find((i) => i.id === initial.instanceId)?.name ?? `#${initial.instanceId}`) : ''}
                </div>
              )}
            </div>

            <div className="space-y-1.5">
              <FieldLabel required={mode === 'create'}>{t('schedules.name')}</FieldLabel>
              {mode === 'create' ? (
                <Input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder={t('schedules.namePlaceholder')}
                  aria-invalid={nameMissing}
                />
              ) : (
                <div className="w-full rounded-md border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
                  {form.name}
                </div>
              )}
            </div>

            <div className="space-y-1.5">
              <FieldLabel required>{t('schedules.cron')}</FieldLabel>
              <Input
                value={form.cronExpr}
                onChange={(e) => setForm({ ...form, cronExpr: e.target.value })}
                placeholder="0 4 * * *"
                className="font-mono"
                aria-invalid={form.cronExpr.length > 0 && !cron.valid}
              />
              {form.cronExpr.length > 0 && !cron.valid ? (
                <p className="text-xs text-destructive">{t(cron.messageKey ?? 'schedules.cronInvalidChar')}</p>
              ) : (
                <p className="text-xs text-muted-foreground">{t('schedules.cronHint')}</p>
              )}
            </div>

            <div className="space-y-1.5">
              <FieldLabel>{t('schedules.action')}</FieldLabel>
              <Combobox
                options={actionOptions}
                value={form.action}
                onChange={(v) => setForm({ ...form, action: v })}
                allowCustom={false}
              />
            </div>

            {form.action === 'command' && (
              <div className="space-y-1.5">
                <FieldLabel>{t('schedules.command')}</FieldLabel>
                <Input
                  value={form.command}
                  onChange={(e) => setForm({ ...form, command: e.target.value })}
                  placeholder="say server restarting"
                  className="font-mono"
                />
                {mode === 'edit' && (
                  <p className="text-xs text-muted-foreground">{t('schedules.commandEditHint')}</p>
                )}
              </div>
            )}

            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={form.enabled}
                onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
              />
              {t('schedules.enabled')}
            </label>
          </ScrollableDialogBody>

          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {submitting
                ? t('common.saving')
                : mode === 'create'
                  ? t('common.create')
                  : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
