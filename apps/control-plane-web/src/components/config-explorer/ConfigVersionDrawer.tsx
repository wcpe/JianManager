// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做三个取数与回滚 mutation 的接线。
import { useState } from 'react'
import ConfigVersionDrawerView from '@/components/views/config-explorer/ConfigVersionDrawer'
import { useConfigVersions, useConfigDiff, useRollbackConfig } from '@/api/configs'

interface ConfigVersionDrawerProps {
  instanceId: number
  /** 当前查看版本的文件相对路径；null 时不渲染内容。 */
  filePath: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 回滚成功后回调。 */
  onRolledBack: () => void
}

/**
 * 配置版本抽屉接线层（ADR-097 b 范式）：版本列表 / diff / 回滚三个请求都在这里，
 * 视图只做受控渲染（原 FR-071 语义：列表、起止对比、一键回滚二次确认）。
 *
 * 对比起止 `diffFrom`/`diffTo` 上提到本层——它们是 diff 查询的键（一变即触发取数），
 * 视图只回传选择、不持状态；`filePath` 变化时与原实现一致地保留选择（组件实例不重挂）。
 *
 * 回滚成功后 `useRollbackConfig` 已失效 `['configs', instanceId]` 缓存并自行提示，
 * 故本层只把成功透成 `onRolledBack` 供配置编辑器刷新，不额外弹 toast。
 * 保留同路径默认导出与同名 props，调用点（ConfigExplorer）无需改动。
 */
export default function ConfigVersionDrawer({
  instanceId,
  filePath,
  open,
  onOpenChange,
  onRolledBack,
}: ConfigVersionDrawerProps) {
  const [diffFrom, setDiffFrom] = useState<number | null>(null)
  const [diffTo, setDiffTo] = useState<number | null>(null)

  const versionsQ = useConfigVersions(instanceId, open ? filePath : null)
  const diffQ = useConfigDiff(instanceId, filePath, diffFrom ?? undefined, diffTo ?? undefined)
  const rollbackMut = useRollbackConfig(instanceId, filePath)

  return (
    <ConfigVersionDrawerView
      filePath={filePath}
      open={open}
      onOpenChange={onOpenChange}
      onRolledBack={onRolledBack}
      versions={versionsQ.data}
      versionsLoading={versionsQ.isLoading}
      versionsFailed={!!versionsQ.error}
      diffFrom={diffFrom}
      diffTo={diffTo}
      onDiffSelect={(from, to) => {
        setDiffFrom(from)
        setDiffTo(to)
      }}
      diff={diffQ.data}
      diffLoading={diffQ.isLoading}
      diffError={diffQ.error ? (diffQ.error as Error).message : undefined}
      onRollback={async (versionId) => {
        // 版本说明沿用原实现（回滚即生成一条新版本记录）。
        await rollbackMut.mutateAsync({ versionId, message: `回滚到 #${versionId}` })
      }}
      rollbackPending={rollbackMut.isPending}
    />
  )
}
