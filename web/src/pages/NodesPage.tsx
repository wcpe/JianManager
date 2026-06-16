import { useTranslation } from 'react-i18next'
import { useNodes } from '@/api/nodes'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

export default function NodesPage() {
  const { t } = useTranslation()
  const { data: nodes, isLoading } = useNodes({ refetchInterval: 30_000 })

  const statusLabel: Record<number, { text: string; color: string }> = {
    0: { text: t('nodes.offline'), color: 'text-red-500' },
    1: { text: t('nodes.online'), color: 'text-green-500' },
    2: { text: t('nodes.starting'), color: 'text-yellow-500' },
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
                <TableHead>{t('nodes.system')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes?.map((node) => {
                const st = statusLabel[node.status] || statusLabel[0]
                return (
                  <TableRow key={node.id}>
                    <TableCell className="font-medium">{node.name}</TableCell>
                    <TableCell className="text-muted-foreground">{node.host}</TableCell>
                    <TableCell>
                      <span className={st.color}>{st.text}</span>
                    </TableCell>
                    <TableCell>{node.cpuUsage ? `${(node.cpuUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell>{node.memoryUsage ? `${(node.memoryUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell>{node.diskUsage ? `${(node.diskUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell className="text-muted-foreground">{node.os} {node.arch}</TableCell>
                  </TableRow>
                )
              })}
              {(!nodes || nodes.length === 0) && (
                <TableRow>
                  <TableCell colSpan={7} className="text-center text-muted-foreground">
                    {t('nodes.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}
