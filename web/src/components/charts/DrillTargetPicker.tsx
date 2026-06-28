/* eslint-disable react-refresh/only-export-components -- 与组件同文件导出类型/纯函数 targetKey，仅影响 Fast Refresh */
import { useTranslation } from 'react-i18next'
import type { InstanceInfo } from '@/api/instances'
import type { NodeInfo } from '@/api/nodes'

/**
 * 下钻目标（FR-221）：平台 → 节点 → 实例 → 世界 逐级钻取。
 * - platform：平台聚合视图。
 * - node：单节点视图（uuid=节点 uuid）。
 * - instance：单实例视图（uuid=实例 uuid，nodeUuid 记录来源节点用于返回上一级）；
 *   world 非空时进一步聚焦到该世界（分世界图只显示该世界）。
 */
export type DrillTarget =
  | { kind: 'platform' }
  | { kind: 'node'; uuid: string }
  | { kind: 'instance'; uuid: string; nodeUuid?: string; world?: string }

/** 取 target 的监控数据源标识（world 不影响取数，只在渲染层过滤）。 */
export function targetKey(t: DrillTarget): string {
  if (t.kind === 'platform') return 'platform'
  if (t.kind === 'node') return `node:${t.uuid}`
  return `instance:${t.uuid}:${t.world ?? ''}`
}

/** 面包屑一节。 */
function Crumb({ label, onClick, active }: { label: string; onClick?: () => void; active?: boolean }) {
  if (active || !onClick) {
    return <span className={active ? 'font-medium text-foreground' : 'text-muted-foreground'}>{label}</span>
  }
  return (
    <button type="button" onClick={onClick} className="text-primary hover:underline">
      {label}
    </button>
  )
}

/**
 * 下钻式目标选择器（FR-221）：面包屑展示当前层级路径 + 当前层提供下钻到下一级的下拉。
 * 替代原扁平 select，使「节点→实例→世界」逐级钻取可达，同时保留任意层一键回跳。
 */
export function DrillTargetPicker({
  target,
  onChange,
  nodes,
  instances,
  worlds,
}: {
  target: DrillTarget
  onChange: (t: DrillTarget) => void
  nodes: NodeInfo[]
  instances: InstanceInfo[]
  /** 当前实例的世界名列表（来自其分世界序列）；instance 层下钻到世界用。 */
  worlds: string[]
}) {
  const { t } = useTranslation()

  const nodeOf = (uuid?: string) => nodes.find((n) => n.uuid === uuid)
  const instOf = (uuid?: string) => instances.find((i) => i.uuid === uuid)

  // 面包屑各节点名。
  const curNodeUuid = target.kind === 'node' ? target.uuid : target.kind === 'instance' ? target.nodeUuid : undefined
  const curNode = nodeOf(curNodeUuid)
  const curInst = target.kind === 'instance' ? instOf(target.uuid) : undefined

  // node 层下钻到实例：仅列挂在该节点上的实例。
  const nodeId = curNode?.id
  const childInstances = instances.filter((i) => i.nodeId === nodeId)

  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
      {/* 面包屑 */}
      <div className="flex flex-wrap items-center gap-1">
        <Crumb
          label={t('monitor.drill.platform')}
          active={target.kind === 'platform'}
          onClick={target.kind !== 'platform' ? () => onChange({ kind: 'platform' }) : undefined}
        />
        {curNode && (
          <>
            <span className="text-muted-foreground/50">/</span>
            <Crumb
              label={curNode.name}
              active={target.kind === 'node'}
              onClick={target.kind !== 'node' ? () => onChange({ kind: 'node', uuid: curNode.uuid }) : undefined}
            />
          </>
        )}
        {curInst && (
          <>
            <span className="text-muted-foreground/50">/</span>
            <Crumb
              label={curInst.name}
              active={target.kind === 'instance' && !target.world}
              onClick={
                target.kind === 'instance' && target.world
                  ? () => onChange({ kind: 'instance', uuid: curInst.uuid, nodeUuid: curNode?.uuid })
                  : undefined
              }
            />
          </>
        )}
        {target.kind === 'instance' && target.world && (
          <>
            <span className="text-muted-foreground/50">/</span>
            <Crumb label={target.world} active />
          </>
        )}
      </div>

      {/* 当前层 → 下钻到下一级的下拉 */}
      {target.kind === 'platform' && nodes.length > 0 && (
        <select
          aria-label={t('monitor.drill.toInstances')}
          value=""
          onChange={(e) => e.target.value && onChange({ kind: 'node', uuid: e.target.value })}
          className="h-7 rounded-md border bg-background px-2 text-xs"
        >
          <option value="">{t('monitor.nodes')}…</option>
          {nodes.map((n) => (
            <option key={n.uuid} value={n.uuid}>
              {n.name}
            </option>
          ))}
        </select>
      )}

      {target.kind === 'node' && childInstances.length > 0 && (
        <select
          aria-label={t('monitor.drill.toInstances')}
          value=""
          onChange={(e) =>
            e.target.value && onChange({ kind: 'instance', uuid: e.target.value, nodeUuid: target.uuid })
          }
          className="h-7 rounded-md border bg-background px-2 text-xs"
        >
          <option value="">{t('monitor.drill.pickInstance')}</option>
          {childInstances.map((i) => (
            <option key={i.uuid} value={i.uuid}>
              {i.name}
            </option>
          ))}
        </select>
      )}

      {target.kind === 'instance' && worlds.length > 0 && (
        <select
          aria-label={t('monitor.drill.toWorlds')}
          value={target.world ?? ''}
          onChange={(e) =>
            onChange({ kind: 'instance', uuid: target.uuid, nodeUuid: target.nodeUuid, world: e.target.value || undefined })
          }
          className="h-7 rounded-md border bg-background px-2 text-xs"
        >
          <option value="">{t('monitor.drill.pickWorld')}</option>
          {worlds.map((w) => (
            <option key={w} value={w}>
              {w}
            </option>
          ))}
        </select>
      )}
    </div>
  )
}
