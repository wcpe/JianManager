// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做列表/漂移取数、三个写动作、组级删除门禁与分组候选接线。
import { useState } from 'react'
import { useBaselineDrift, useConfigBaselines, useConvergeBaseline, useDeleteBaseline, useUpsertBaseline } from '@/api/configBaselines'
import { useInstanceGroups } from '@/api/instanceGroups'
import { useDangerPermission } from '@/lib/shared/danger'
import { ConfigBaselinesPageView } from '@/components/views/config-baselines/ConfigBaselinesPageView'

/**
 * 配置基线页容器（ADR-097 b 范式）：基线列表、漂移明细与分组候选的取数，保存 / 删除 / 收敛三个写动作，
 * 组级删除门禁都在这里决定——列表、漂移面板与编辑弹窗交共享视图。
 * 三个 mutation 的成功/失败提示由 `@/api/configBaselines` 统一给出（与原实现同源，此处不重复弹）。
 *
 * 受控状态归属：`driftBaselineId` 归容器——它是 `useBaselineDrift` 的查询键，一变就触发取数；
 * 编辑弹窗的开合与草稿留在视图内。分组候选只有编辑弹窗的 `group:<id>` 选择器用到，而弹窗开合归视图所有，
 * 故分组树改为页面挂载即取一次（原实现是「编辑弹窗挂载才请求」）——只为这一处选择器，代价是一次小请求。
 *
 * 保留同路径默认导出，路由表（`ROUTE_CHUNKS['/config-baselines']`）与既有用例无需改动。
 */
export default function ConfigBaselinesPage() {
  const { data: baselines, isLoading } = useConfigBaselines()
  const upsert = useUpsertBaseline()
  const del = useDeleteBaseline()
  // 展开漂移面板的基线 ID；null 表示收起（此时 useBaselineDrift 不发请求）。
  const [driftBaselineId, setDriftBaselineId] = useState<number | null>(null)
  const { data: drift, isLoading: driftLoading, refetch, isFetching: driftFetching } = useBaselineDrift(driftBaselineId)
  const converge = useConvergeBaseline()
  // `group:<id>` scope 选择器的候选：组织树分组（ADR-033），与用户组 id 正交。
  const { data: groups } = useInstanceGroups()
  // 删除是组级破坏操作：角色门禁在应用侧判定后注入（包内不持鉴权状态）。
  const { allowed: dangerAllowed } = useDangerPermission('group')

  return (
    <ConfigBaselinesPageView
      baselines={baselines}
      isLoading={isLoading}
      groups={groups}
      driftBaselineId={driftBaselineId}
      onToggleDrift={(id) => setDriftBaselineId((current) => (current === id ? null : id))}
      drift={drift}
      driftLoading={driftLoading}
      driftFetching={driftFetching}
      onRefreshDrift={() => { void refetch() }}
      converging={converge.isPending}
      saving={upsert.isPending}
      deleting={del.isPending}
      dangerAllowed={dangerAllowed}
      // 创建与编辑同走 upsert（后端按 scopeKey+filePath 覆盖），故无需消费 editingId。
      // 归一化在此完成：filePath 去首尾空格、message 为空则省略（与原实现提交的落库体逐字一致）。
      onSubmit={async (values) => {
        try {
          await upsert.mutateAsync({
            scopeKey: values.scopeKey,
            filePath: values.filePath.trim(),
            content: values.content,
            message: values.message.trim() || undefined,
          })
          return true
        } catch {
          // 失败提示由 useUpsertBaseline 的 onError 给出；返回 false 让视图保留草稿与窗口。
          return false
        }
      }}
      onConverge={async (baselineId) => {
        try {
          const res = await converge.mutateAsync({ baselineId })
          return { targeted: res.targeted, succeeded: res.succeeded, failed: res.failed }
        } catch {
          // 与原实现一致：收敛失败不额外提示、也不回显汇总（视图保留上次结果）。
          return null
        }
      }}
      onDelete={async (baseline) => {
        try {
          await del.mutateAsync(baseline.id)
          return true
        } catch {
          // 失败提示由 useDeleteBaseline 的 onError 给出；返回 false 让确认框保持打开。
          return false
        }
      }}
    />
  )
}
