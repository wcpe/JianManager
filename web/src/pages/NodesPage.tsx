import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useNodes,
  useSetNodeMaintenance,
  useDrainNode,
  useDeleteNode,
  type NodeInfo,
} from '@/api/nodes'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'

import { useState } from 'react'
import NodeJDKPanel from '@/components/NodeJDKPanel'
import NodePortsPanel from '@/components/NodePortsPanel'
import DangerConfirm from '@/components/DangerConfirm'

/** 将字节数格式化为人类可读的大小（B/KB/MB/GB）。 */
function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

/** 待二次确认的危险节点操作（FR-048）。 */
type PendingAction = { kind: 'drain' | 'delete'; node: NodeInfo }

export default function NodesPage() {
  const { t } = useTranslation()
  const { data: nodes, isLoading } = useNodes({ refetchInterval: 30_000 })

  const [jdkNodeId, setJdkNodeId] = useState<number | null>(null)
  const [portsNodeId, setPortsNodeId] = useState<number | null>(null)
  const [pending, setPending] = useState<PendingAction | null>(null)

  const setMaintenance = useSetNodeMaintenance()
  const drain = useDrainNode()
  const del = useDeleteNode()

  const statusLabel: Record<number, { text: string; color: string }> = {
    0: { text: t('nodes.offline'), color: 'text-red-500' },
    1: { text: t('nodes.online'), color: 'text-green-500' },
    2: { text: t('nodes.starting'), color: 'text-yellow-500' },
  }

  const toggleMaintenance = (node: NodeInfo) => {
    const enabled = !node.maintenance
    setMaintenance.mutate(
      { id: node.id, enabled },
      {
        onSuccess: () =>
          toast.success(enabled ? t('nodes.maintenanceEnabled') : t('nodes.maintenanceDisabled')),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      },
    )
  }

  const confirmPending = () => {
    if (!pending) return
    const { kind, node } = pending
    setPending(null)
    if (kind === 'drain') {
      drain.mutate(node.id, {
        onSuccess: (res) =>
          toast.success(t('nodes.drainDone', { count: res.data.stoppedCount })),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      })
    } else {
      del.mutate(node.id, {
        onSuccess: () => toast.success(t('nodes.deleted')),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      })
    }
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">{t('nodes.title')}</h1>
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('nodes.name')}</TableHead>
                <TableHead>{t('nodes.ip')}</TableHead>
                <TableHead>{t('nodes.status')}</TableHead>
                <TableHead>{t('nodes.cpu')}</TableHead>
                <TableHead>{t('nodes.memory')}</TableHead>
                <TableHead>{t('nodes.disk')}</TableHead>
                <TableHead>{t('nodes.network')}</TableHead>
                <TableHead>{t('nodes.system')}</TableHead>
                <TableHead className="text-right">{t('nodes.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes?.map((node) => {
                const st = statusLabel[node.status] || statusLabel[0]
                const isOnline = node.status === 1
                return (
                  <TableRow key={node.id}>
                    <TableCell className="font-medium">{node.name}</TableCell>
                    <TableCell className="text-muted-foreground">{node.host}</TableCell>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <span className={st.color}>{st.text}</span>
                        {node.maintenance && (
                          <Badge variant="outline" className="text-amber-600 border-amber-500/50">
                            {t('nodes.maintenance')}
                          </Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell>{node.cpuUsage ? `${(node.cpuUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell>{node.memoryUsage ? `${(node.memoryUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell>{node.diskUsage ? `${(node.diskUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {node.networkBytesSent || node.networkBytesRecv
                        ? `↑${formatBytes(node.networkBytesSent)} ↓${formatBytes(node.networkBytesRecv)}`
                        : '--'}
                    </TableCell>
                    <TableCell className="text-muted-foreground">{node.os} {node.arch}</TableCell>
                    <TableCell className="text-right space-x-3 whitespace-nowrap">
                      <button
                        className="text-xs text-blue-600 hover:underline"
                        onClick={() => setJdkNodeId(node.id)}
                      >
                        JDK
                      </button>
                      <button
                        className="text-xs text-blue-600 hover:underline"
                        onClick={() => setPortsNodeId(node.id)}
                      >
                        {t('ports.button')}
                      </button>
                      <button
                        className="text-xs text-amber-600 hover:underline"
                        onClick={() => toggleMaintenance(node)}
                      >
                        {node.maintenance ? t('nodes.uncordon') : t('nodes.cordon')}
                      </button>
                      <button
                        className="text-xs text-orange-600 hover:underline disabled:opacity-40 disabled:no-underline"
                        onClick={() => setPending({ kind: 'drain', node })}
                      >
                        {t('nodes.drain')}
                      </button>
                      <button
                        className="text-xs text-red-600 hover:underline disabled:opacity-40 disabled:no-underline"
                        disabled={isOnline}
                        title={isOnline ? t('nodes.deleteOnlineHint') : undefined}
                        onClick={() => setPending({ kind: 'delete', node })}
                      >
                        {t('nodes.delete')}
                      </button>
                    </TableCell>
                  </TableRow>
                )
              })}
              {(!nodes || nodes.length === 0) && (
                <TableRow>
                  <TableCell colSpan={9} className="text-center text-muted-foreground">
                    {t('nodes.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}
      {jdkNodeId !== null && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border rounded-lg p-6 w-full max-w-2xl shadow-lg max-h-[80vh] overflow-y-auto">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-lg font-bold">JDK Management</h2>
              <button onClick={() => setJdkNodeId(null)} className="text-sm text-muted-foreground hover:text-foreground">Close</button>
            </div>
            <NodeJDKPanel nodeId={jdkNodeId} />
          </div>
        </div>
      )}
      {portsNodeId !== null && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border rounded-lg p-6 w-full max-w-2xl shadow-lg max-h-[80vh] overflow-y-auto">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-lg font-bold">{t('ports.title')}</h2>
              <button onClick={() => setPortsNodeId(null)} className="text-sm text-muted-foreground hover:text-foreground">{t('common.close')}</button>
            </div>
            <NodePortsPanel nodeId={portsNodeId} />
          </div>
        </div>
      )}
      <DangerConfirm
        open={pending !== null}
        title={pending?.kind === 'drain' ? t('nodes.drainConfirmTitle') : t('nodes.deleteConfirmTitle')}
        description={
          pending?.kind === 'drain'
            ? t('nodes.drainConfirmDesc', { name: pending?.node.name })
            : t('nodes.deleteConfirmDesc', { name: pending?.node.name })
        }
        confirmLabel={pending?.kind === 'drain' ? t('nodes.drain') : t('nodes.delete')}
        confirmText={pending?.kind === 'delete' ? pending?.node.name : undefined}
        scope="platform"
        onConfirm={confirmPending}
        onCancel={() => setPending(null)}
      />
    </div>
  )
}
