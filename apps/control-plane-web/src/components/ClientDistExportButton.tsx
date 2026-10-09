// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只注入导出实现与结果回执（受控化）。
import { toast } from 'sonner'
import { ClientDistExportButton as ClientDistExportButtonView } from '@/components/views/client-dist/EmbeddedUpdaterParts'
import { exportClientDistCSV, saveClientDistCSV } from '@/api/clientDistExport'
import type { ClientDistExportFilters, ClientDistExportKind } from '@/api/clientDistExport'

interface ClientDistExportButtonProps {
  kind: ClientDistExportKind
  filters: ClientDistExportFilters
  size?: 'xs' | 'sm' | 'default' | 'lg' | 'icon' | 'icon-sm' | 'icon-lg'
}

/** 分发数据导出按钮的接线层：注入导出端点与 toast 回执。 */
export default function ClientDistExportButton({ kind, filters, size = 'sm' }: ClientDistExportButtonProps) {
  return (
    <ClientDistExportButtonView
      kind={kind}
      filters={filters}
      size={size}
      onExport={async (k, f) => {
        const result = await exportClientDistCSV(k as ClientDistExportKind, f as ClientDistExportFilters)
        saveClientDistCSV(result.blob, result.filename)
      }}
      onNotify={(level, message) => (level === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
