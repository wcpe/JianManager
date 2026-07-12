import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ArrowUpCircle, Loader2, Package, RefreshCw, Search, Trash2 } from 'lucide-react'
import {
  useGlobalPackages,
  useInstallGlobalPackage,
  useRemoveGlobalPackage,
} from '@/api/pmConfig'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Badge } from '@jianmanager/ui/components/badge'
import DangerConfirm from '@/components/DangerConfirm'

/**
 * 节点全局包管理（FR-307）：托管全局目录（<数据根>/opt/runtimes/global）内已装包的
 * 列表 / 搜索 / 安装 / 升级 / 卸载。安装与升级为任务中心异步（202+taskId，进度看任务中心）；
 * 卸载同步。包管理器与下载源取节点 FR-306 配置，此处不重复选择。
 */
export default function NodeGlobalPackagesSection({ nodeId, active = true }: { nodeId: number; active?: boolean }) {
  const { t } = useTranslation()
  const { data, isLoading, isFetching, refetch, error } = useGlobalPackages(nodeId, { enabled: active })
  const install = useInstallGlobalPackage(nodeId)
  const removePkg = useRemoveGlobalPackage(nodeId)

  const [query, setQuery] = useState('')
  const [name, setName] = useState('')
  const [version, setVersion] = useState('')
  const [pendingDel, setPendingDel] = useState<string | null>(null)

  if (!active) return null

  const pkgs = (data?.packages ?? []).filter((p) => !query.trim() || p.name.toLowerCase().includes(query.trim().toLowerCase()))

  const submitInstall = (pkgName: string, ver?: string) => {
    install.mutate(
      { name: pkgName.trim(), version: ver?.trim() || undefined },
      {
        onSuccess: () => {
          toast.success(t('pkg.installSubmitted', '安装任务已提交，进度见任务中心'))
          setName('')
          setVersion('')
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(err.response?.data?.message || t('pkg.installFailed', '提交安装失败')),
      },
    )
  }

  return (
    <div className="space-y-3 rounded-lg border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Package className="size-4 text-muted-foreground" />
        <span className="text-sm font-medium">{t('pkg.title', '全局包')}</span>
        {data?.pm && <Badge variant="outline" className="text-xs">{data.pm}</Badge>}
        <span className="flex-1" />
        <Button variant="ghost" size="sm" onClick={() => refetch()} disabled={isFetching} aria-label={t('common.refresh', '刷新')}>
          {isFetching ? <Loader2 className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}
        </Button>
      </div>

      {/* 安装表单：包名 + 可选版本（空=latest）。 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="mineflayer 或 @scope/pkg"
          aria-label={t('pkg.nameLabel', '包名')}
          className="h-8 w-56 text-xs"
        />
        <Input
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          placeholder={t('pkg.versionPlaceholder', '版本（空=latest）')}
          aria-label={t('pkg.versionLabel', '版本')}
          className="h-8 w-36 text-xs"
        />
        <Button size="sm" disabled={install.isPending || !name.trim()} onClick={() => submitInstall(name, version)}>
          {install.isPending ? <Loader2 className="size-4 animate-spin" /> : t('pkg.install', '安装')}
        </Button>
      </div>

      {/* 列表：名称 / 版本 / 可更新徽章 + 升级 / 卸载。 */}
      {isLoading ? (
        <div className="text-sm text-muted-foreground">{t('common.loading', '加载中…')}</div>
      ) : error ? (
        <div className="text-sm text-status-danger">
          {(error as Error & { response?: { data?: { message?: string } } }).response?.data?.message || t('pkg.listFailed', '获取全局包失败')}
        </div>
      ) : (
        <>
          {(data?.packages?.length ?? 0) > 3 && (
            <div className="relative">
              <Search className="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input value={query} onChange={(e) => setQuery(e.target.value)}
                placeholder={t('pkg.searchPlaceholder', '搜索包名')} className="h-8 pl-8 text-xs" />
            </div>
          )}
          {pkgs.length === 0 ? (
            <div className="rounded-md border border-dashed p-4 text-center text-xs text-muted-foreground">
              {t('pkg.empty', '托管全局目录还没有安装任何包（bot 依赖如 mineflayer 装在这里）')}
            </div>
          ) : (
            <div className="space-y-1.5">
              {pkgs.map((p) => (
                <div key={p.name} className="flex items-center gap-2.5 rounded-md border bg-card px-3 py-2 text-sm transition-colors hover:bg-muted/40">
                  <span className="min-w-0 flex-1 truncate font-mono text-xs" title={p.name}>{p.name}</span>
                  <span className="text-xs text-muted-foreground">{p.version}</span>
                  {p.latest && (
                    <Badge variant="outline" className="border-status-warning/50 text-xs text-status-warning" title={t('pkg.updatableTo', { version: p.latest, defaultValue: '可更新到 {{version}}' })}>
                      {p.latest}
                    </Badge>
                  )}
                  {p.latest && (
                    <button
                      type="button"
                      aria-label={t('pkg.upgrade', '升级')}
                      className="shrink-0 text-muted-foreground transition-colors hover:text-primary"
                      onClick={() => submitInstall(p.name)}
                    >
                      <ArrowUpCircle className="size-4" />
                    </button>
                  )}
                  <button
                    type="button"
                    aria-label={t('pkg.remove', '卸载')}
                    className="shrink-0 text-muted-foreground transition-colors hover:text-status-danger"
                    onClick={() => setPendingDel(p.name)}
                  >
                    <Trash2 className="size-4" />
                  </button>
                </div>
              ))}
            </div>
          )}
        </>
      )}

      <DangerConfirm
        open={pendingDel !== null}
        title={t('pkg.removeTitle', '卸载全局包?')}
        description={t('pkg.removeDesc', { name: pendingDel ?? '', defaultValue: '将从托管全局目录卸载 {{name}}（可随时重装）。' })}
        confirmLabel={t('pkg.remove', '卸载')}
        onConfirm={() => {
          const n = pendingDel!
          setPendingDel(null)
          removePkg.mutate(n, {
            onSuccess: () => toast.success(t('pkg.removed', '已卸载')),
            onError: (err: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(err.response?.data?.message || t('pkg.removeFailed', '卸载失败')),
          })
        }}
        onCancel={() => setPendingDel(null)}
      />
    </div>
  )
}
