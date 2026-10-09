/**
 * @file DatabaseExplorerView：数据库资源管理器的受控外壳，隔离应用取数与表选择策略。
 * @input lib/db-contracts、lib/utils、翻译上下文、图标与应用注入的表清单和行视图
 * @output DatabaseExplorerView、DatabaseExplorerViewProps
 * @sync apps/control-plane-web/src/components/database/DatabaseExplorer.tsx、apps/control-plane-web/src/pages/DatabasePage.dom.test.tsx
 * @since FR-502
 */
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Database } from 'lucide-react'
import type { DbTableInfo } from '@/lib/database/db-contracts'
import { cn } from '@jianmanager/ui/lib/utils'

/** 表选择与取数由应用容器维护，包内仅展示左侧表树和右侧内容。 */
export interface DatabaseExplorerViewProps {
  tables?: DbTableInfo[]
  tablesLoading: boolean
  tablesError: boolean
  activeTable: string
  onSelectTable: (table: string) => void
  children?: ReactNode
}

export function DatabaseExplorerView({
  tables,
  tablesLoading,
  tablesError,
  activeTable,
  onSelectTable,
  children,
}: DatabaseExplorerViewProps) {
  const { t } = useTranslation()

  return (
    <div className="flex min-h-0 flex-1 gap-4">
      {/* 左：表树 */}
      <aside className="flex w-56 shrink-0 flex-col rounded-lg border bg-card/40">
        <div className="flex items-center gap-2 border-b px-3 py-2 text-sm font-medium">
          <Database className="size-4" />
          <span>{t('database.tables')}</span>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-1">
          {tablesLoading && (
            <p className="px-2 py-3 text-xs text-muted-foreground">{t('common.loading')}</p>
          )}
          {tablesError && (
            <p className="px-2 py-3 text-xs text-destructive">{t('database.tablesError')}</p>
          )}
          {!tablesLoading && !tablesError && (tables?.length ?? 0) === 0 && (
            <p className="px-2 py-3 text-xs text-muted-foreground">{t('database.noTables')}</p>
          )}
          {tables?.map((tb) => (
            <button
              key={tb.name}
              type="button"
              onClick={() => onSelectTable(tb.name)}
              className={cn(
                'flex w-full items-center justify-between gap-2 rounded px-2 py-1.5 text-left text-[13px] transition-colors',
                tb.name === activeTable
                  ? 'bg-primary/10 font-medium text-primary'
                  : 'text-foreground/80 hover:bg-accent/60',
              )}
            >
              <span className="truncate font-mono">{tb.name}</span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {tb.rowCount < 0 ? '?' : tb.rowCount}
              </span>
            </button>
          ))}
        </div>
      </aside>

      {/* 右：行浏览 */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        {activeTable ? (
          children
        ) : (
          <div className="grid flex-1 place-items-center text-sm text-muted-foreground">
            {t('database.selectTable')}
          </div>
        )}
      </div>
    </div>
  )
}
