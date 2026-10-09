import { useState } from 'react'
import { toast } from 'sonner'

import VersionDrawer from '@/components/views/explorer/VersionDrawer'
import { useFileVersions, useFileVersionDiff, useRollbackFile } from '@/api/fileVersions'

/**
 * 历史版本抽屉接线层（ADR-097）：三个取数 hook 留在这里，视图只做受控渲染。
 *
 * `diffFrom`/`diffTo` 由视图上报（它们是视图内的选择态），这里只用来驱动 diff 查询——
 * 查询自身的 enabled 条件（两者都非空且不同）与原实现一致。
 */
type ConnectedProps = Omit<
  Parameters<typeof VersionDrawer>[0],
  | 'versions'
  | 'versionsLoading'
  | 'versionsFailed'
  | 'onDiffSelect'
  | 'diff'
  | 'diffLoading'
  | 'diffError'
  | 'onRollback'
  | 'rollbackPending'
  | 'notify'
> & {
  /** 实例 ID（视图不需要它——取数都在这一层）。 */
  instanceId: number
}

export default function VersionDrawerConnected(props: ConnectedProps) {
  const { instanceId, filePath, open } = props
  const [diffFrom, setDiffFrom] = useState<number | null>(null)
  const [diffTo, setDiffTo] = useState<number | null>(null)

  const versionsQ = useFileVersions(instanceId, open ? filePath : null)
  const diffQ = useFileVersionDiff(instanceId, filePath, diffFrom ?? undefined, diffTo ?? undefined)
  const rollbackMut = useRollbackFile(instanceId, filePath)

  return (
    <VersionDrawer
      {...props}
      versions={versionsQ.data}
      versionsLoading={versionsQ.isLoading}
      versionsFailed={!!versionsQ.error}
      onDiffSelect={(from, to) => {
        setDiffFrom(from)
        setDiffTo(to)
      }}
      diff={diffQ.data}
      diffLoading={diffQ.isLoading}
      diffError={diffQ.error ? (diffQ.error as Error).message : undefined}
      onRollback={async (versionId) => {
        await rollbackMut.mutateAsync({ versionId })
      }}
      rollbackPending={rollbackMut.isPending}
      notify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
