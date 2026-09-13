import { Fragment, useEffect, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Clock, Pencil, ScrollText, Trash2 } from 'lucide-react'
import {
  useSchedules,
  useCreateSchedule,
  useUpdateSchedule,
  useDeleteSchedule,
  useScheduleLogs,
  type ScheduleInfo,
} from '@/api/schedules'
import { useInstances } from '@/api/instances'
import { validateCron, nextRuns, describeCron, CRON_PRESETS } from '@/lib/cron'
import {
  ConfigRow,
  ConfigSwitch,
  ConfigViewToggle,
  ConfigSummaryChips,
  type ConfigView,
} from '@/pages/config-row'
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
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import {
  Table,
  TableBody,
  TableCard,
  TableCardFooter,
  TableCardHeader,
  TableCell,
  TableEmptyRow,
  TableHead,
  TableHeader,
  TableIconButton,
  TableRow,
  TableSkeletonRows,
} from '@jianmanager/ui/components/table'
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
  const [view, setView] = useState<ConfigView>('list')
  // 汇总条筛选：'enabled' 仅启用 / 'disabled' 仅停用 / null 全部。
  const [filter, setFilter] = useState<'enabled' | 'disabled' | null>(null)

  const instanceName = (id: number) =>
    instances?.find((i) => i.id === id)?.name ?? `#${id}`

  // cron 人类可读文案（FR-153）：可识别则译，否则退回原表达式。
  const cronReadable = (expr: string): string => {
    const desc = describeCron(expr)
    return desc ? t(desc.key, desc.params) : expr
  }

  const enabledCount = (schedules ?? []).filter((s) => s.enabled).length
  const visible = (schedules ?? []).filter((s) =>
    filter === 'enabled' ? s.enabled : filter === 'disabled' ? !s.enabled : true,
  )

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
      {/* 汇总筛选条保持在卡片外（作用于列表视图的可见行）。 */}
      <ConfigSummaryChips
        chips={[
          { label: t('schedules.summaryAll'), value: (schedules ?? []).length, active: filter === null, onClick: () => setFilter(null) },
          {
            label: t('schedules.summaryEnabled'),
            value: enabledCount,
            tone: 'success',
            active: filter === 'enabled',
            onClick: () => setFilter(filter === 'enabled' ? null : 'enabled'),
          },
          {
            label: t('schedules.summaryDisabled'),
            value: (schedules ?? []).length - enabledCount,
            tone: 'neutral',
            active: filter === 'disabled',
            onClick: () => setFilter(filter === 'disabled' ? null : 'disabled'),
          },
        ]}
      />

      {/* 方案 A「精工卡片」：标题/计数/主操作进卡片头，表格 refined 外观，底部汇总。 */}
      <TableCard>
        <TableCardHeader
          title={t('schedules.title')}
          count={t('schedules.cardCount', { total: (schedules ?? []).length })}
          actions={
            <>
              <ConfigViewToggle view={view} onChange={setView} cardLabel={t('common.cardView')} listLabel={t('common.listView')} />
              <Button onClick={() => setShowCreate(true)}>+ {t('schedules.createSchedule')}</Button>
            </>
          }
        />

        {isLoading ? (
          <Table appearance="refined">
            <TableHeader>
              <TableRow>
                <TableHead>{t('schedules.name')}</TableHead>
                <TableHead>{t('schedules.instance')}</TableHead>
                <TableHead>{t('schedules.cron')}</TableHead>
                <TableHead>{t('schedules.action')}</TableHead>
                <TableHead>{t('schedules.enabled')}</TableHead>
                <TableHead align="right">{t('schedules.lastRun')}</TableHead>
                <TableHead align="right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableSkeletonRows rows={4} cols={7} />
            </TableBody>
          </Table>
        ) : visible.length === 0 ? (
          <Table appearance="refined">
            <TableHeader>
              <TableRow>
                <TableHead>{t('schedules.name')}</TableHead>
                <TableHead>{t('schedules.instance')}</TableHead>
                <TableHead>{t('schedules.cron')}</TableHead>
                <TableHead>{t('schedules.action')}</TableHead>
                <TableHead>{t('schedules.enabled')}</TableHead>
                <TableHead align="right">{t('schedules.lastRun')}</TableHead>
                <TableHead align="right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableEmptyRow colSpan={7} icon={<Clock />} title={t('schedules.empty')} description={t('schedules.emptyHint')} />
            </TableBody>
          </Table>
        ) : view === 'card' ? (
          <div className="flex flex-col gap-2.5 p-3">
            {visible.map((s) => (
              <ConfigRow
                key={s.id}
                icon={<Clock className="size-[18px]" />}
                tone={s.enabled ? 'primary' : 'neutral'}
                title={s.name}
                code={s.cronExpr}
                subtitle={`${instanceName(s.instanceId)} · ${t(`schedules.action_${s.action}`, { defaultValue: s.action })} · ${cronReadable(s.cronExpr)}`}
                meta={
                  <>
                    <div>{s.enabled ? t('schedules.nextRunLabel') : t('schedules.disabledLabel')}</div>
                    <div>
                      {s.enabled
                        ? validateCron(s.cronExpr).valid
                          ? (nextRuns(s.cronExpr, 1)[0]?.toLocaleString() ?? t('schedules.notScheduled'))
                          : t('schedules.notScheduled')
                        : s.lastRun
                          ? new Date(s.lastRun).toLocaleString()
                          : t('schedules.neverRun')}
                    </div>
                  </>
                }
                trailing={
                  <>
                    <ConfigSwitch
                      checked={s.enabled}
                      onChange={() => handleToggleEnabled(s)}
                      label={t('schedules.enabled')}
                      onLabel={t('schedules.enable')}
                      offLabel={t('schedules.disable')}
                    />
                    <Button variant="ghost" size="xs" onClick={() => setLogsId(logsId === s.id ? null : s.id)}>
                      {logsId === s.id ? t('schedules.hideLogs') : t('schedules.viewLogs')}
                    </Button>
                    <Button variant="ghost" size="xs" onClick={() => setEditing(s)}>
                      {t('common.edit')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      className="text-status-danger hover:text-status-danger"
                      onClick={() => setDeleteTarget(s)}
                    >
                      {t('common.delete')}
                    </Button>
                  </>
                }
              />
            ))}
            {logsId !== null && visible.some((s) => s.id === logsId) && (
              <Panel bodyClassName="p-0">
                <ScheduleLogs scheduleId={logsId} />
              </Panel>
            )}
          </div>
        ) : (
          <Table appearance="refined" stickyHeader containerClassName="max-h-[70vh] overflow-y-auto">
            <TableHeader>
              <TableRow>
                <TableHead>{t('schedules.name')}</TableHead>
                <TableHead>{t('schedules.instance')}</TableHead>
                <TableHead>{t('schedules.cron')}</TableHead>
                <TableHead>{t('schedules.action')}</TableHead>
                <TableHead>{t('schedules.enabled')}</TableHead>
                <TableHead align="right">{t('schedules.lastRun')}</TableHead>
                <TableHead align="right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visible.map((s) => {
                const expanded = logsId === s.id
                return (
                  <Fragment key={s.id}>
                    <TableRow>
                      <TableCell className="font-medium text-foreground">{s.name}</TableCell>
                      <TableCell className="text-muted-foreground">{instanceName(s.instanceId)}</TableCell>
                      <TableCell className="font-mono text-xs">
                        <div>{s.cronExpr}</div>
                        <div className="font-sans text-muted-foreground">{cronReadable(s.cronExpr)}</div>
                      </TableCell>
                      <TableCell>{t(`schedules.action_${s.action}`, { defaultValue: s.action })}</TableCell>
                      <TableCell>
                        <ConfigSwitch
                          checked={s.enabled}
                          onChange={() => handleToggleEnabled(s)}
                          label={t('schedules.enabled')}
                          onLabel={t('schedules.enable')}
                          offLabel={t('schedules.disable')}
                        />
                      </TableCell>
                      <TableCell align="right" className="text-muted-foreground tabular-nums">
                        {s.lastRun ? new Date(s.lastRun).toLocaleString() : t('schedules.neverRun')}
                      </TableCell>
                      {/* 3 个操作 → 图标按钮（方案 A：28px 命中区 / 15px 图标）；aria-label 保留原文案可达名。 */}
                      <TableCell align="right">
                        <div className="flex items-center justify-end gap-1">
                          <TableIconButton
                            aria-label={expanded ? t('schedules.hideLogs') : t('schedules.viewLogs')}
                            title={expanded ? t('schedules.hideLogs') : t('schedules.viewLogs')}
                            aria-expanded={expanded}
                            onClick={() => setLogsId(expanded ? null : s.id)}
                          >
                            <ScrollText />
                          </TableIconButton>
                          <TableIconButton
                            aria-label={t('common.edit')}
                            title={t('common.edit')}
                            onClick={() => setEditing(s)}
                          >
                            <Pencil />
                          </TableIconButton>
                          <TableIconButton
                            tone="danger"
                            aria-label={t('common.delete')}
                            title={t('common.delete')}
                            onClick={() => setDeleteTarget(s)}
                          >
                            <Trash2 />
                          </TableIconButton>
                        </div>
                      </TableCell>
                    </TableRow>
                    {expanded && (
                      <TableRow data-flat>
                        <TableCell colSpan={7} className="p-0">
                          <ScheduleLogs scheduleId={s.id} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                )
              })}
            </TableBody>
          </Table>
        )}

        <TableCardFooter>
          {t('schedules.cardSummary', {
            total: (schedules ?? []).length,
            enabled: enabledCount,
            disabled: (schedules ?? []).length - enabledCount,
          })}
        </TableCardFooter>
      </TableCard>

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
  // 合法时给出可读描述与下次执行预览（FR-153）；预览按浏览器本地时区，仅供直觉参考。
  const cronDesc = useMemo(() => (cron.valid ? describeCron(form.cronExpr) : null), [cron.valid, form.cronExpr])
  const cronNext = useMemo(() => (cron.valid ? nextRuns(form.cronExpr, 3) : []), [cron.valid, form.cronExpr])
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
              <div className="flex flex-wrap items-center gap-1">
                <span className="text-xs text-muted-foreground">{t('schedules.presets')}:</span>
                {CRON_PRESETS.map((p) => (
                  <button
                    key={p.expr}
                    type="button"
                    onClick={() => setForm({ ...form, cronExpr: p.expr })}
                    className="rounded border px-2 py-0.5 text-xs text-muted-foreground hover:bg-accent"
                  >
                    {t(p.labelKey)}
                  </button>
                ))}
              </div>
              <Input
                value={form.cronExpr}
                onChange={(e) => setForm({ ...form, cronExpr: e.target.value })}
                placeholder="0 4 * * *"
                className="font-mono"
                aria-invalid={form.cronExpr.length > 0 && !cron.valid}
              />
              {form.cronExpr.length > 0 && !cron.valid ? (
                <p className="text-xs text-destructive">{t(cron.messageKey ?? 'schedules.cronInvalidChar')}</p>
              ) : cron.valid ? (
                <div className="space-y-1 text-xs text-muted-foreground">
                  {cronDesc && <p className="text-foreground">{t(cronDesc.key, cronDesc.params)}</p>}
                  {cronNext.length > 0 && (
                    <p>{t('schedules.nextRuns')}: {cronNext.map((d) => d.toLocaleString()).join(' · ')}</p>
                  )}
                  <p className="text-muted-foreground/70">{t('schedules.previewTzNote')}</p>
                </div>
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
