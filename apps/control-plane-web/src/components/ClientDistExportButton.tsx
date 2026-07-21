import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@jianmanager/ui/components/button'
import {
  exportClientDistCSV,
  saveClientDistCSV,
  type ClientDistExportFilters,
  type ClientDistExportKind,
} from '@/api/clientDistExport'

interface ClientDistExportButtonProps {
  kind: ClientDistExportKind
  filters: ClientDistExportFilters
  size?: 'xs' | 'sm' | 'default' | 'lg' | 'icon' | 'icon-sm' | 'icon-lg'
}

export default function ClientDistExportButton({ kind, filters, size = 'sm' }: ClientDistExportButtonProps) {
  const { t } = useTranslation()
  const [exporting, setExporting] = useState(false)

  const runExport = async () => {
    setExporting(true)
    try {
      const result = await exportClientDistCSV(kind, filters)
      saveClientDistCSV(result.blob, result.filename)
      toast.success(t('clientDistExport.success'))
    } catch (error) {
      const status = (error as { response?: { status?: number } }).response?.status
      toast.error(status === 429 ? t('clientDistExport.rateLimited') : t('clientDistExport.failed'))
    } finally {
      setExporting(false)
    }
  }

  return (
    <Button type="button" variant="outline" size={size} disabled={exporting} onClick={runExport}>
      <Download className="size-3.5" />
      {exporting ? t('clientDistExport.exporting') : t('clientDistExport.button')}
    </Button>
  )
}
