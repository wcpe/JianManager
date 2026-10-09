/**
 * @file SchedulesPageView：定时任务页的受控视图（列表 + 执行日志 + 创建/编辑弹窗），取数、四个写动作与 toast 由应用容器负责。
 * @input lib/cron（cron 校验/人类可读描述/预设）、lib/schedule-form（表单状态与初值）、lib/schedule（ScheduleInfo）、
 *        views/config-explorer/ConfigRow（卡片行/开关/视图切换/汇总筛选条）、views/DangerConfirm（删除二次确认）、
 *        Table/Panel/Dialog/StatusBadge 等原语、翻译上下文
 * @output SchedulesPageView、SchedulesPageViewProps、ScheduleFilter、ScheduleLogRow、ScheduleLogsView、
 *         ScheduleLogsViewProps、ScheduleFormDialogView、ScheduleFormDialogViewProps、SchedulesInstancePickerArgs
 * @sync apps/control-plane-web/src/pages/SchedulesPage.tsx、apps/control-plane-web/src/pages/SchedulesPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-012 定时任务、FR-153 cron 可读文案与命令编辑、FR-207 mock 域簇）
 */
import { Fragment, useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Clock, Pencil, ScrollText, Trash2 } from 'lucide-react'
import {
  ConfigRow,
  ConfigSwitch,
  ConfigSummaryChips,
  ConfigViewToggle,
  type ConfigView,
} from '@/components/views/config-explorer/ConfigRow'
import DangerConfirm from '@/components/views/DangerConfirm'
import { Button } from '@jianmanager/ui/components/button'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
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
import { CRON_PRESETS, describeCron, nextRuns, validateCron } from '@/lib/cron'
import {
  EMPTY_SCHEDULE_FORM,
  SCHEDULE_ACTIONS,
  formFromSchedule,
  type ScheduleFormState,
} from '@/lib/schedule-form'
import type { ScheduleInfo } from '@/lib/schedule'

/** 汇总筛选条取值：'enabled' 仅启用 / 'disabled' 仅停用 / null 全部。 */
export type ScheduleFilter = 'enabled' | 'disabled' | null

/**
 * 执行日志行（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/schedules` 的 `ScheduleLogInfo`：
 * 容器直接传 API 返回的完整对象也结构兼容，无需把该 API 类型迁进包
 * （同 `BackupStorageRow`、`ArtifactVersionView` 的取舍）。
 */
export interface ScheduleLogRow {
  id: number
  /** 触发动作：start / stop / restart / command / backup。 */
  action: string
  /** 执行结果：success / failed。 */
  status: string
  /** 失败原因（成功时为空串）。 */
  error: string
  startedAt: string
}

/** 实例选择器插槽参数（千级实例须服务端搜索，故实现由外壳注入）。 */
export interface SchedulesInstancePickerArgs {
  /** 当前选中的实例 id；未选为 null。 */
  value: number | null
  /** 选中变更：外壳据此回填表单草稿（草稿归本组件，值域归外壳实现）。 */
  onChange: (id: number | null) => void
  /** 必填校验失败态：外壳应透传给选择器的 invalid。 */
  invalid: boolean
  /** 是否取数：弹窗关闭时为 false，外壳据此停发请求。 */
  enabled: boolean
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 列表、日志与加载态经 props 注入（容器调 `useSchedules` / `useScheduleLogs`）；
 * - 四个写动作以回调上报，由容器执行 mutation 并决定成功/失败文案：提交回传 `Promise`，
 *   视图据返回值决定是否关窗（失败时保留草稿与窗口便于重试）；
 * - **归容器**的受控状态：汇总筛选 `filter`（决定可见行集合）与日志展开 `logsId`
 *   （它是 `useScheduleLogs` 的查询键，一变就触发取数）——两者都是「列表在看什么」这一
 *   查询语义，容器是其唯一持有者；`logsId` 的清理（删除当前展开行时收起）也在容器完成，
 *   因为只有容器同时知道删除目标与展开目标；
 * - **留本组件**的纯 UI 状态：创建/编辑弹窗开合与编辑目标、删除目标、卡片/列表视图切换；
 *   `ScheduleFormDialogView` 内的表单草稿同样留包内（打开瞬间按模式回填或清空）；
 * - 实例候选走 `renderInstancePicker` 插槽：选择器本体在包内（`views/instances/InstancePicker`），
 *   但「何时发请求、防抖多久」是外壳策略，故由外壳提供实现。
 */
export interface SchedulesPageViewProps {
  /** 定时任务列表；容器取数后注入（缺省或空数组渲染空态）。 */
  schedules?: ScheduleInfo[]
  /** 列表加载态（渲染骨架行）。 */
  isLoading?: boolean
  /** 汇总筛选条当前值（只影响可见行，不触发取数）。 */
  filter: ScheduleFilter
  /** 汇总筛选条变更上报。 */
  onFilterChange: (filter: ScheduleFilter) => void
  /** 当前展开查看日志的任务 ID；null 表示全部收起。 */
  logsId: number | null
  /** 展开/收起某任务的执行日志（同一 id 再次触发即为收起，由容器翻转）。 */
  onToggleLogs: (id: number) => void
  /** 已展开任务的执行日志行（容器按 `logsId` 取数后注入）。 */
  logs?: ScheduleLogRow[]
  /** 执行日志加载态。 */
  logsLoading?: boolean
  /** 创建在途：禁用创建弹窗的提交按钮。 */
  creating?: boolean
  /** 更新在途：禁用编辑弹窗的提交按钮。 */
  updating?: boolean
  /** 危险操作（删除）是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  dangerAllowed?: boolean
  /** 提交表单；`editingId` 为 null 表示创建。返回是否成功——成功才关窗。 */
  onSubmit: (form: ScheduleFormState, editingId: number | null) => Promise<boolean>
  /** 启用/停用切换上报（容器执行 PUT 并决定提示文案）。 */
  onToggleEnabled: (schedule: ScheduleInfo) => void
  /** 删除已确认的任务（二次确认已在本视图内完成）。 */
  onDelete: (schedule: ScheduleInfo) => void
  /** 实例选择器插槽；创建弹窗依赖它选实例，故为必填。 */
  renderInstancePicker: (args: SchedulesInstancePickerArgs) => ReactNode
}

/**
 * 调度行显示的实例名。
 *
 * 后端已在 `/schedules` 回填 `instanceName`（见 `service.ScheduleView`），故直接用行上的字段，
 * 不再全量拉实例列表做 id→name 反查——那在千级规模下是约 1MB/轮的开销，且带 30 秒兜底轮询。
 * 实例被删时后端留空，这里回退显示 #id。
 */
function instanceNameOf(s: { instanceId: number; instanceName?: string }): string {
  return s.instanceName ?? `#${s.instanceId}`
}

/** 执行日志展开区（FR-012 展示）：时间 / 结果 / 输出三态由容器注入，本组件只渲染。 */
export interface ScheduleLogsViewProps {
  /** 日志行；缺省或空数组渲染「暂无日志」。 */
  items?: ScheduleLogRow[]
  /** 加载态。 */
  isLoading?: boolean
}

export function ScheduleLogsView({ items, isLoading = false }: ScheduleLogsViewProps) {
  const { t } = useTranslation()

  return (
    <div className="bg-muted/30 p-3">
      {isLoading ? (
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      ) : !items || items.length === 0 ? (
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
            {items.map((log) => (
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

/** 创建/编辑定时任务对话框的受控契约：`open` 与 `submitting` 由外壳决定，草稿与校验留本组件。 */
export interface ScheduleFormDialogViewProps {
  open: boolean
  /** create：可选实例/名称；edit：实例/名称只读，仅改 cron/action/启用。 */
  mode: 'create' | 'edit'
  /** 编辑模式的回填来源（打开瞬间一次性回填）。 */
  initial?: ScheduleInfo | null
  /** 提交在途：禁用提交按钮并显示保存中文案。 */
  submitting?: boolean
  /** 关闭/取消回调（提交成功后本组件也会调用它）。 */
  onClose: () => void
  /** 提交表单；返回是否成功——成功才关窗（失败时保留草稿便于重试）。 */
  onSubmit: (form: ScheduleFormState) => Promise<boolean>
  /** 实例选择器插槽（仅创建模式使用；编辑模式实例只读展示）。 */
  renderInstancePicker: (args: SchedulesInstancePickerArgs) => ReactNode
}

/** 创建/编辑定时任务对话框（含 Cron 基本校验与 command 条件输入，FR-153）。 */
export function ScheduleFormDialogView({
  open,
  mode,
  initial,
  submitting = false,
  onClose,
  onSubmit,
  renderInstancePicker,
}: ScheduleFormDialogViewProps) {
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

  const actionOptions: ComboboxOption[] = SCHEDULE_ACTIONS.map((a) => ({ value: a, label: t(`schedules.action_${a}`) }))

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    // 失败（false，容器已弹 toast）时不关窗、不清草稿，便于修正后重试。
    const ok = await onSubmit(form)
    if (ok) onClose()
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
                renderInstancePicker({
                  value: form.instanceId ? Number(form.instanceId) : null,
                  onChange: (id) => setForm({ ...form, instanceId: id === null ? '' : String(id) }),
                  invalid: instanceMissing,
                  enabled: open,
                })
              ) : (
                <div className="w-full rounded-md border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
                  {initial ? instanceNameOf(initial) : ''}
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

/**
 * 定时任务页（FR-012 计划任务，FR-153 cron 可读文案）。
 * 列表支持卡片/列表两种视图与汇总筛选，行内可启停、展开执行日志、编辑、删除。
 */
export function SchedulesPageView({
  schedules,
  isLoading = false,
  filter,
  onFilterChange,
  logsId,
  onToggleLogs,
  logs,
  logsLoading = false,
  creating = false,
  updating = false,
  dangerAllowed,
  onSubmit,
  onToggleEnabled,
  onDelete,
  renderInstancePicker,
}: SchedulesPageViewProps) {
  const { t } = useTranslation()

  const [showCreate, setShowCreate] = useState(false)
  // 正在编辑的任务（null 表示未编辑）。编辑目标不触发取数（取数由容器的 mutation 成功钩子失效缓存驱动）。
  const [editing, setEditing] = useState<ScheduleInfo | null>(null)
  // 待删除确认的任务。
  const [deleteTarget, setDeleteTarget] = useState<ScheduleInfo | null>(null)
  const [view, setView] = useState<ConfigView>('list')

  // cron 人类可读文案（FR-153）：可识别则译，否则退回原表达式。
  const cronReadable = (expr: string): string => {
    const desc = describeCron(expr)
    return desc ? t(desc.key, desc.params) : expr
  }

  const enabledCount = (schedules ?? []).filter((s) => s.enabled).length
  const visible = (schedules ?? []).filter((s) =>
    filter === 'enabled' ? s.enabled : filter === 'disabled' ? !s.enabled : true,
  )

  return (
    // 阶段 6 页面迁移 + 全量对齐：外壳与页头改用布局层原语。
    // 标题按布局规范上移到页面级 PageHeader——「页名只出现一次」且「在页面最上面」，
    // 卡片头随之只留计数与操作（TableCardHeader 的 title 已改为可选）。
    // PageHeader 必须是第一个子元素，故汇总筛选条排到它之后。
    <PageShell data-page="schedules">
      <PageHeader title={t('schedules.title')} />

      {/* 汇总筛选条保持在卡片外（作用于列表视图的可见行）。 */}
      <ConfigSummaryChips
        chips={[
          { label: t('schedules.summaryAll'), value: (schedules ?? []).length, active: filter === null, onClick: () => onFilterChange(null) },
          {
            label: t('schedules.summaryEnabled'),
            value: enabledCount,
            tone: 'success',
            active: filter === 'enabled',
            onClick: () => onFilterChange(filter === 'enabled' ? null : 'enabled'),
          },
          {
            label: t('schedules.summaryDisabled'),
            value: (schedules ?? []).length - enabledCount,
            tone: 'neutral',
            active: filter === 'disabled',
            onClick: () => onFilterChange(filter === 'disabled' ? null : 'disabled'),
          },
        ]}
      />

      {/* 方案 A「精工卡片」：标题/计数/主操作进卡片头，表格 refined 外观，底部汇总。 */}
      <TableCard>
        <TableCardHeader
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
                subtitle={`${instanceNameOf(s)} · ${t(`schedules.action_${s.action}`, { defaultValue: s.action })} · ${cronReadable(s.cronExpr)}`}
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
                      onChange={() => onToggleEnabled(s)}
                      label={t('schedules.enabled')}
                      onLabel={t('schedules.enable')}
                      offLabel={t('schedules.disable')}
                    />
                    <Button variant="ghost" size="xs" onClick={() => onToggleLogs(s.id)}>
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
                <ScheduleLogsView items={logs} isLoading={logsLoading} />
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
                      <TableCell className="text-muted-foreground">{instanceNameOf(s)}</TableCell>
                      <TableCell className="font-mono text-xs">
                        <div>{s.cronExpr}</div>
                        <div className="font-sans text-muted-foreground">{cronReadable(s.cronExpr)}</div>
                      </TableCell>
                      <TableCell>{t(`schedules.action_${s.action}`, { defaultValue: s.action })}</TableCell>
                      <TableCell>
                        <ConfigSwitch
                          checked={s.enabled}
                          onChange={() => onToggleEnabled(s)}
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
                            onClick={() => onToggleLogs(s.id)}
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
                          <ScheduleLogsView items={logs} isLoading={logsLoading} />
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
      <ScheduleFormDialogView
        open={showCreate}
        mode="create"
        submitting={creating}
        renderInstancePicker={renderInstancePicker}
        onClose={() => setShowCreate(false)}
        onSubmit={(form) => onSubmit(form, null)}
      />

      {/* 编辑对话框：后端仅接收 cron/action/enabled。 */}
      <ScheduleFormDialogView
        open={editing !== null}
        mode="edit"
        initial={editing}
        submitting={updating}
        renderInstancePicker={renderInstancePicker}
        onClose={() => setEditing(null)}
        onSubmit={(form) => (editing ? onSubmit(form, editing.id) : Promise.resolve(false))}
      />

      {/* 删除二次确认：scope/allowed 语义与原页一致——scope 固定 group，是否放行由容器注入。 */}
      <DangerConfirm
        open={deleteTarget !== null}
        title={t('schedules.deleteTitle', { name: deleteTarget?.name ?? '' })}
        description={t('schedules.deleteDesc')}
        confirmLabel={t('common.delete')}
        scope="group"
        allowed={dangerAllowed}
        onConfirm={() => {
          if (!deleteTarget) return
          const target = deleteTarget
          setDeleteTarget(null)
          onDelete(target)
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </PageShell>
  )
}

export default SchedulesPageView
