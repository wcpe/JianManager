import { useTranslation } from 'react-i18next'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useNodePorts } from '@/api/ports'

/** 节点端口占用面板（FR-032）：展示系统已分配的 server/query 端口与分配范围（RCON 已退役 FR-067）。 */
export default function NodePortsPanel({ nodeId }: { nodeId: number }) {
  const { t } = useTranslation()
  const { data, isLoading } = useNodePorts(nodeId)

  if (isLoading) return <p className="text-muted-foreground text-sm">{t('common.loading')}</p>

  return (
    <div>
      {data && (
        <p className="text-xs text-muted-foreground mb-3">
          {t('ports.range', {
            server: data.ranges.serverPortBase,
            size: data.ranges.rangeSize,
          })}
        </p>
      )}
      <div className="border rounded-md">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('ports.instance')}</TableHead>
              <TableHead>{t('ports.role')}</TableHead>
              <TableHead>{t('ports.serverPort')}</TableHead>
              <TableHead>{t('ports.queryPort')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.occupied.map((p) => (
              <TableRow key={p.instanceId}>
                <TableCell className="font-medium">{p.name}</TableCell>
                <TableCell>{t(`networks.role_${p.role}`, { defaultValue: p.role })}</TableCell>
                <TableCell>{p.serverPort || '--'}</TableCell>
                <TableCell>{p.queryPort || '--'}</TableCell>
              </TableRow>
            ))}
            {(!data || data.occupied.length === 0) && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  {t('ports.empty')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
