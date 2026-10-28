import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowUpCircle, Package, Search, Trash2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { RefreshButton } from '@jianmanager/ui/components/refresh-button'
import { Card } from '@jianmanager/ui/components/card'
import { Input } from '@jianmanager/ui/components/input'
import { Badge } from '@jianmanager/ui/components/badge'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { Spinner } from '@jianmanager/ui/components/spinner'
import DangerConfirm from '@/components/views/common/DangerConfirm'

/**
 * 一条全局包（本组件所需的最小结构）。
 *
 * 外壳直接传 `@/api/pmConfig` 的 `GlobalPackage` 会结构兼容，故无需把该 API 类型迁进包。
 */
export interface GlobalPackageView {
  name: string
  version: string
  /** 可更新到的版本；无则不可更新。 */
  latest?: string
}

/** 全局包视图（同上）。 */
export interface GlobalPackagesView {
  /** 包管理器名（展示用徽章）。 */
  pm: string
  packages: GlobalPackageView[]
}

/**
 * 节点全局包管理（FR-307）：托管全局目录（<数据根>/opt/runtimes/global）内已装包的
 * 列表 / 搜索 / 安装 / 升级 / 卸载。安装与升级为任务中心异步（202+taskId，进度看任务中心）；
 * 卸载同步。包管理器与下载源取节点 FR-306 配置，此处不重复选择。
 *
 * 受控视图（ADR-103 b 范式）：不取数、不发请求、不弹 toast——数据与刷新/安装/卸载
 * 一律经 props 与回调进出；搜索词、安装表单草稿、待删包名等 UI 状态留在组件内。
 * 是否挂载（对应原先的 `active`）由外壳决定，组件不再自行判断。
 */
export interface NodeGlobalPackagesSectionProps {
  /** 列表数据；外壳取数后注入。 */
  data?: GlobalPackagesView
  /** 首次加载态（渲染骨架）。 */
  isLoading?: boolean
  /** 后台刷新中（刷新按钮转圈）。 */
  isFetching?: boolean
  /** 列表读取失败的后端消息；外壳注入。 */
  errorMessage?: string
  /** 手动刷新。 */
  onRefresh: () => void
  /** 安装或升级到指定版本（version 缺省=latest）。返回是否成功。 */
  onInstall: (name: string, version?: string) => Promise<boolean>
  /** 卸载。返回是否成功。 */
  onRemove: (name: string) => Promise<boolean>
  /** 安装在途：禁用安装按钮。 */
  installing?: boolean
}

export default function NodeGlobalPackagesSection({
  data,
  isLoading,
  isFetching = false,
  errorMessage,
  onRefresh,
  onInstall,
  onRemove,
  installing = false,
}: NodeGlobalPackagesSectionProps) {
  const { t } = useTranslation()

  const [query, setQuery] = useState('')
  const [name, setName] = useState('')
  const [version, setVersion] = useState('')
  const [pendingDel, setPendingDel] = useState<string | null>(null)

  const pkgs = (data?.packages ?? []).filter((p) => !query.trim() || p.name.toLowerCase().includes(query.trim().toLowerCase()))

  const submitInstall = async (pkgName: string, ver?: string) => {
    const ok = await onInstall(pkgName, ver)
    // 成功才清空表单（失败保留输入，便于用户修正后重试）。
    if (ok) {
      setName('')
      setVersion('')
    }
  }

  return (
    <div className="space-y-3 rounded-lg border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Package className="size-4 text-muted-foreground" />
        <span className="text-sm font-medium">{t('pkg.title', '全局包')}</span>
        {data?.pm && <Badge variant="outline" className="text-xs">{data.pm}</Badge>}
        <span className="flex-1" />
        {/*
          刷新：原先「在途显 Spinner / 否则显刷新图标」的三元，由 `pending` 旋转图标统一覆盖。
          `size="sm"` 显式保留：原按钮就是这个盒子尺寸，纯图标态默认会取 `icon-sm`。
        */}
        <RefreshButton
          variant="ghost"
          size="sm"
          pending={isFetching}
          onClick={onRefresh}
          aria-label={t('common.refresh', '刷新')}
        />
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
        <Button size="sm" disabled={installing || !name.trim()} onClick={() => void submitInstall(name, version)}>
          {installing ? <Spinner /> : t('pkg.install', '安装')}
        </Button>
      </div>

      {/* 列表：名称 / 版本 / 可更新徽章 + 升级 / 卸载。 */}
      {isLoading ? (
        /* 骨架占位：按包行（px-3 py-2 单行文本 ≈ h-10）轮廓，替代裸文字。 */
        <div className="space-y-1.5">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : errorMessage ? (
        <div className="text-sm text-status-danger">{errorMessage}</div>
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
                /* 列表行：Card 只作统一外壳，行内布局与 hover 涂层原样保留。 */
                <Card key={p.name} className="flex-row items-center gap-2.5 px-3 py-2 text-sm transition-colors hover:bg-muted/40">
                  <span className="min-w-0 flex-1 truncate font-mono text-xs" title={p.name}>{p.name}</span>
                  <span className="text-xs text-muted-foreground">{p.version}</span>
                  {p.latest && (
                    <Badge variant="outline" className="border-status-warning/50 text-xs text-status-warning" title={t('pkg.updatableTo', { version: p.latest, defaultValue: '可更新到 {{version}}' })}>
                      {p.latest}
                    </Badge>
                  )}
                  {p.latest && (
                    // 纯图标升级 → ghost + icon-xs
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label={t('pkg.upgrade', '升级')}
                      className="shrink-0 text-muted-foreground hover:text-primary"
                      onClick={() => void submitInstall(p.name)}
                    >
                      <ArrowUpCircle className="size-4" />
                    </Button>
                  )}
                  {/* 纯图标卸载 → ghost + icon-xs */}
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={t('pkg.remove', '卸载')}
                    className="shrink-0 text-muted-foreground hover:text-status-danger"
                    onClick={() => setPendingDel(p.name)}
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </Card>
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
          void onRemove(n)
        }}
        onCancel={() => setPendingDel(null)}
      />
    </div>
  )
}
