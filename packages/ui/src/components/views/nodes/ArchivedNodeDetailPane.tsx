import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Server } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { ObjectPageHeader } from '@jianmanager/ui/components/shell'
import type { ArchivedNode } from '../../../lib/node-types'

export interface ArchivedNodeDetailPaneProps {
  node: ArchivedNode
  onPurge: () => void
  purging: boolean
  /** 面包屑导航（容器注入路由跳转；组件库不依赖 react-router）。 */
  onNavigate: (to: string) => void
}
/** 归档只读详情 + 清理按钮（FR-393/394）。 */
export function ArchivedNodeDetailPane({
  node,
  onPurge,
  purging,
  onNavigate,
}: ArchivedNodeDetailPaneProps) {
  const { t } = useTranslation()
  const when = node.deletedAt ? new Date(node.deletedAt).toLocaleString() : '--'
  const rows: { label: string; value: ReactNode }[] = [
    { label: t('nodes.ip'), value: node.host },
    { label: t('nodes.system'), value: `${node.os || '--'} ${node.arch || ''}`.trim() },
    { label: t('nodes.cpuCores'), value: node.cpuCores > 0 ? node.cpuCores : '--' },
    { label: t('nodes.deletedAt'), value: when },
    { label: 'UUID', value: <span className="font-mono text-xs break-all">{node.uuid}</span> },
  ]
  return (
    <div className="space-y-3">
      {/* 阶段 6 收尾：归档详情与活跃详情用同一对象头形态（此处无分段工具，故不传 tools）。
          归档徽标走 status，「清理」走 actions，host 走 meta。 */}
      <ObjectPageHeader
        breadcrumbs={[
          { label: t('nodes.title'), to: '/nodes' },
          { label: node.name },
        ]}
        icon={<Server className="size-5" />}
        title={node.name}
        status={{ tone: 'default', label: t('nodes.viewArchive') }}
        meta={[{ label: t('nodes.ip'), value: node.host }]}
        actions={
          <Button variant="destructive" size="sm" onClick={onPurge} disabled={purging}>
            {t('nodes.purge')}
          </Button>
        }
        onNavigate={onNavigate}
      />
      <Panel title={t('nodes.overviewSection')}>
        <dl className="grid gap-2 sm:grid-cols-2">
          {rows.map((r) => (
            <div key={r.label} className="rounded-md border px-3 py-2">
              <dt className="text-[11px] text-muted-foreground">{r.label}</dt>
              <dd className="mt-0.5 text-sm">{r.value}</dd>
            </div>
          ))}
        </dl>
      </Panel>
    </div>
  )
}
