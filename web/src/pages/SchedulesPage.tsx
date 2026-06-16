import { useTranslation } from 'react-i18next'
import { useSchedules } from '@/api/schedules'
import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

export default function SchedulesPage() {
  const { t } = useTranslation()
  const { data: schedules, isLoading } = useSchedules()

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">{t('schedules.title')}</h1>
      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('schedules.name')}</TableHead>
                <TableHead>{t('schedules.instanceId')}</TableHead>
                <TableHead>{t('schedules.cron')}</TableHead>
                <TableHead>{t('schedules.action')}</TableHead>
                <TableHead>{t('schedules.enabled')}</TableHead>
                <TableHead>{t('schedules.lastRun')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {schedules?.map((s) => (
                <TableRow key={s.id}>
                  <TableCell className="font-medium">{s.name}</TableCell>
                  <TableCell className="text-muted-foreground">{s.instanceId}</TableCell>
                  <TableCell className="font-mono text-xs">{s.cronExpr}</TableCell>
                  <TableCell>{s.action}</TableCell>
                  <TableCell>
                    <Badge variant={s.enabled ? 'default' : 'secondary'}>
                      {s.enabled ? t('common.enabled') : t('common.disabled')}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {s.lastRun ? new Date(s.lastRun).toLocaleString() : t('schedules.neverRun')}
                  </TableCell>
                </TableRow>
              ))}
              {(!schedules || schedules.length === 0) && (
                <TableRow><TableCell colSpan={6} className="text-center text-muted-foreground">{t('schedules.empty')}</TableCell></TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}
