// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做列表/日志取数、四个写动作、组级删除门禁与实例选择器接线。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useSchedules, useCreateSchedule, useUpdateSchedule, useDeleteSchedule, useScheduleLogs } from '@/api/schedules'
import { InstancePicker } from '@/components/InstancePicker'
import { useDangerPermission } from '@/lib/danger'
import { toCreateBody, toUpdateBody } from '@/lib/schedule-form'
import { SchedulesPageView } from '@/components/views/schedules/SchedulesPageView'
import type { ScheduleFilter } from '@/components/views/schedules/SchedulesPageView'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 定时任务页容器（ADR-097 b 范式）：列表与执行日志取数、创建/更新/删除三个写动作、
 * 组级删除门禁与 toast 文案都在这里决定，列表、日志与两个对话框交共享视图。
 *
 * 两处受控状态归容器：`filter`（决定可见行集合）与 `logsId`（`useScheduleLogs` 的查询键，
 * 一变就触发取数）——它们都是「列表在看什么」这一查询语义，故不留在视图里。
 * 实例候选按千级规模走服务端搜索，故选择器实现经插槽注入。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function SchedulesPage() {
  const { t } = useTranslation()
  const { data: schedules, isLoading } = useSchedules()

  const createSchedule = useCreateSchedule()
  const updateSchedule = useUpdateSchedule()
  const deleteSchedule = useDeleteSchedule()

  // 汇总筛选：'enabled' 仅启用 / 'disabled' 仅停用 / null 全部。
  const [filter, setFilter] = useState<ScheduleFilter>(null)
  // 展开查看日志的任务 ID；null 表示全部收起（此时 useScheduleLogs 不发请求）。
  const [logsId, setLogsId] = useState<number | null>(null)
  const { data: logs, isLoading: logsLoading } = useScheduleLogs(logsId)

  // 删除是组级破坏操作：角色门禁在应用侧判定后注入（包内不持鉴权状态）。
  const { allowed: dangerAllowed } = useDangerPermission('group')

  return (
    <SchedulesPageView
      schedules={schedules}
      isLoading={isLoading}
      filter={filter}
      onFilterChange={setFilter}
      logsId={logsId}
      onToggleLogs={(id) => setLogsId((current) => (current === id ? null : id))}
      logs={logs?.items}
      logsLoading={logsLoading}
      creating={createSchedule.isPending}
      updating={updateSchedule.isPending}
      dangerAllowed={dangerAllowed}
      onSubmit={async (form, editingId) => {
        try {
          if (editingId !== null) {
            // 后端仅接收 cron/action/enabled（command 动作额外带 payload）。
            await updateSchedule.mutateAsync({ id: editingId, body: toUpdateBody(form) })
            toast.success(t('schedules.updatedToast'))
          } else {
            await createSchedule.mutateAsync(toCreateBody(form))
            toast.success(t('schedules.createdToast'))
          }
          // 成功：视图据此关窗。
          return true
        } catch (err) {
          toast.error(errMessage(err, editingId !== null ? t('common.error') : t('schedules.createFailed')))
          return false
        }
      }}
      onToggleEnabled={(s) => {
        updateSchedule.mutate(
          { id: s.id, body: { enabled: !s.enabled } },
          {
            onSuccess: () => toast.success(s.enabled ? t('schedules.disabledToast') : t('schedules.enabledToast')),
            onError: (err: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(errMessage(err, t('common.error'))),
          },
        )
      }}
      onDelete={(s) => {
        // 删掉的正是当前展开行时一并收起日志：只有容器同时知道删除目标与展开目标。
        if (logsId === s.id) setLogsId(null)
        deleteSchedule.mutate(s.id, {
          onSuccess: () => toast.success(t('schedules.deletedToast')),
          onError: (err: Error & { response?: { data?: { message?: string } } }) =>
            toast.error(errMessage(err, t('common.error'))),
        })
      }}
      renderInstancePicker={({ value, onChange, invalid, enabled }) => (
        <InstancePicker
          value={value}
          onChange={onChange}
          enabled={enabled}
          placeholder={t('schedules.selectInstance')}
          invalid={invalid}
        />
      )}
    />
  )
}
