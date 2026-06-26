import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { ArrowLeft, ChevronDown, ChevronRight, Package, ScrollText, Search, Wrench } from 'lucide-react'

import { useLicenses, type LicenseDependency } from '@/api/licenses'
import { depKey, filterByName, partitionDeps } from '@/lib/licenses'
import { Panel } from '@/components/ui/panel'
import { StatCard } from '@/components/ui/stat-card'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

/** 开源许可与依赖清单页（FR-135）：搜索 + 运行时/开发分区计数（StatCard）+ 表格 + 行内展开许可证全文。 */
export default function LicensesPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data, isLoading, isError } = useLicenses()
  const [query, setQuery] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const deps = useMemo(() => data?.dependencies ?? [], [data])
  const filtered = useMemo(() => filterByName(deps, query), [deps, query])
  const part = useMemo(() => partitionDeps(filtered), [filtered])

  const toggle = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <Button variant="ghost" size="sm" onClick={() => navigate(-1)} className="gap-1.5">
          <ArrowLeft className="size-4" />
          {t('licenses.back')}
        </Button>
        <div className="min-w-0">
          <h1 className="text-xl font-bold">{t('licenses.title')}</h1>
          {data?.generatedAt && (
            <p className="text-xs text-muted-foreground">
              {t('licenses.generatedAt', { time: new Date(data.generatedAt).toLocaleString() })}
            </p>
          )}
        </div>
      </div>

      <p className="text-sm text-muted-foreground">{t('licenses.subtitle')}</p>

      {!isLoading && !isError && deps.length > 0 && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <StatCard
            label={t('licenses.statTotal')}
            value={part.total}
            icon={<Package className="size-3.5" />}
          />
          <StatCard
            label={t('licenses.runtime')}
            value={part.runtimeCount}
            icon={<Package className="size-3.5" />}
            tone="success"
          />
          <StatCard
            label={t('licenses.dev')}
            value={part.devCount}
            icon={<Wrench className="size-3.5" />}
            tone="info"
          />
          <StatCard
            label={t('licenses.statLicenses')}
            value={part.licenseCount}
            icon={<ScrollText className="size-3.5" />}
          />
        </div>
      )}

      <div className="relative max-w-sm">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('licenses.searchPlaceholder')}
          aria-label={t('licenses.searchPlaceholder')}
          className="pl-8"
        />
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError || deps.length === 0 ? (
        <Panel bodyClassName="py-10">
          <p className="text-center text-sm text-muted-foreground">{t('licenses.empty')}</p>
        </Panel>
      ) : (
        <>
          <DependencyTable
            title={t('licenses.runtime')}
            icon={<Package className="size-3.5" />}
            tone="success"
            deps={part.runtime}
            expanded={expanded}
            onToggle={toggle}
          />
          <DependencyTable
            title={t('licenses.dev')}
            icon={<Wrench className="size-3.5" />}
            tone="info"
            deps={part.dev}
            expanded={expanded}
            onToggle={toggle}
          />
        </>
      )}
    </div>
  )
}

/** 单分区依赖表（运行时 / 开发）：行内可展开查看许可证全文。 */
function DependencyTable({
  title,
  icon,
  tone,
  deps,
  expanded,
  onToggle,
}: {
  title: string
  icon: ReactNode
  tone: 'success' | 'info'
  deps: LicenseDependency[]
  expanded: Set<string>
  onToggle: (key: string) => void
}) {
  const { t } = useTranslation()
  return (
    <Panel
      title={
        <span className="flex items-center gap-1.5">
          {title}
          <Badge variant="secondary" className="text-[10px] tabular-nums">
            {deps.length}
          </Badge>
        </span>
      }
      icon={icon}
      tone={tone}
      bodyClassName="p-0"
    >
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-8" />
            <TableHead>{t('licenses.colName')}</TableHead>
            <TableHead className="w-32">{t('licenses.colVersion')}</TableHead>
            <TableHead className="w-40">{t('licenses.colLicense')}</TableHead>
            <TableHead>{t('licenses.colAuthor')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {deps.length === 0 ? (
            <TableRow>
              <TableCell colSpan={5} className="py-6 text-center text-muted-foreground">
                {t('licenses.empty')}
              </TableCell>
            </TableRow>
          ) : (
            deps.map((d) => {
              const key = depKey(d)
              const isOpen = expanded.has(key)
              const isLink = /^https?:\/\//.test(d.url)
              return (
                <Fragment key={key}>
                  <TableRow
                    className="cursor-pointer transition-colors duration-200 ease-ios"
                    onClick={() => onToggle(key)}
                  >
                    <TableCell className="text-muted-foreground">
                      {isOpen ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
                    </TableCell>
                    <TableCell className="font-medium">
                      <span className="flex items-center gap-2">
                        {isLink ? (
                          <a
                            href={d.url}
                            target="_blank"
                            rel="noreferrer"
                            onClick={(e) => e.stopPropagation()}
                            className="text-primary hover:underline"
                          >
                            {d.name}
                          </a>
                        ) : (
                          d.name
                        )}
                        <Badge variant="outline" className="shrink-0 text-[10px] font-normal">
                          {d.scope}
                        </Badge>
                      </span>
                    </TableCell>
                    <TableCell className="tabular-nums text-muted-foreground">{d.version || '—'}</TableCell>
                    <TableCell>
                      {d.license ? (
                        <Badge variant="secondary" className="font-normal">
                          {d.license}
                        </Badge>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell className="max-w-64 truncate text-muted-foreground">{d.author || '—'}</TableCell>
                  </TableRow>
                  {isOpen && (
                    <TableRow className="hover:bg-transparent">
                      <TableCell colSpan={5} className="bg-muted/30">
                        {d.licenseText ? (
                          <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-background p-3 text-xs leading-relaxed text-foreground/90">
                            {d.licenseText}
                          </pre>
                        ) : (
                          <p className="text-xs text-muted-foreground">{t('licenses.noLicenseText')}</p>
                        )}
                      </TableCell>
                    </TableRow>
                  )}
                </Fragment>
              )
            })
          )}
        </TableBody>
      </Table>
    </Panel>
  )
}
