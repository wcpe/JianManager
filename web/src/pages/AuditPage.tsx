import { useTranslation } from 'react-i18next'
import { useAuditLogs } from '@/api/audit'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

export default function AuditPage() {
  const { t } = useTranslation()
  const { data: logs, isLoading } = useAuditLogs({ limit: 100 })

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">{t('audit.title')}</h1>
      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg overflow-hidden">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('audit.time')}</TableHead>
                <TableHead>{t('audit.user')}</TableHead>
                <TableHead>{t('audit.action')}</TableHead>
                <TableHead>{t('audit.target')}</TableHead>
                <TableHead>{t('audit.ip')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {logs?.map((log) => (
                <TableRow key={log.id}>
                  <TableCell className="text-muted-foreground whitespace-nowrap">
                    {new Date(log.createdAt).toLocaleString()}
                  </TableCell>
                  <TableCell>{log.user?.username ?? `#${log.userId}`}</TableCell>
                  <TableCell>
                    <span className="px-2 py-0.5 text-xs bg-muted rounded font-mono">{log.action}</span>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {log.targetType && `${log.targetType}#${log.targetId}`}
                  </TableCell>
                  <TableCell className="text-muted-foreground">{log.ip}</TableCell>
                </TableRow>
              ))}
              {(!logs || logs.length === 0) && (
                <TableRow><TableCell colSpan={5} className="text-center text-muted-foreground">{t('audit.empty')}</TableCell></TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}
