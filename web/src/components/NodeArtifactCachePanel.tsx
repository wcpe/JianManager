import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Trash2, Copy, Database, FileArchive, Search } from 'lucide-react'
import {
  useArtifactCache,
  useEvictArtifactCache,
  useClearArtifactCache,
  useSetArtifactCacheCap,
  type ArtifactCacheItem,
} from '@/api/nodeRuntime'
import { formatCacheBytes, capGiBToBytes, capBytesToGiB, describeCap } from '@/lib/artifact-cache'
import { copyToClipboard } from '@/lib/clipboard'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import DangerConfirm from '@/components/DangerConfirm'

/**
 * 节点制品缓存面板（FR-178）：列缓存项（名/版本/大小/最近用）+ 总占用 + 容量上限设置 + 清/逐项清。
 *
 * 真·节点级（性能优化）：Worker 按 sha256 缓存下载过的核心 jar，建实例命中即秒拷免重下。
 * 全局制品库管理仍归控制面板（FR-082），此面板只看/清这份本地缓存。
 * 可复用独立组件（不绑死容器，便于 FR-177 改挂右栏分段）。
 */
interface NodeArtifactCachePanelProps {
  nodeId: number
  /** 是否启用查询（抽屉/分段打开时为 true，避免后台轮询离屏节点）。 */
  active?: boolean
}

/** 把 Unix 秒格式化为本地日期时间；0/空回「—」。 */
function fmtTime(sec: number): string {
  if (!sec) return '—'
  return new Date(sec * 1000).toLocaleString()
}

export default function NodeArtifactCachePanel({ nodeId, active = true }: NodeArtifactCachePanelProps) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useArtifactCache(nodeId, { enabled: active })
  const evict = useEvictArtifactCache(nodeId)
  const clear = useClearArtifactCache(nodeId)
  const setCap = useSetArtifactCacheCap(nodeId)

  const [capInput, setCapInput] = useState('')
  const [capDirty, setCapDirty] = useState(false)
  const [pendingEvict, setPendingEvict] = useState<ArtifactCacheItem | null>(null)
  const [confirmClear, setConfirmClear] = useState(false)
  const [query, setQuery] = useState('')

  // 上限输入：未编辑时回显服务端值（GB）；编辑后用本地值（稳定区，不切换隐显）。
  const capValue = capDirty ? capInput : capBytesToGiB(data?.capBytes ?? 0)

  const onSaveCap = () => {
    const bytes = capGiBToBytes(capValue)
    setCap.mutate(bytes, {
      onSuccess: () => {
        toast.success(t('artifactCache.capSaved', { cap: describeCap(bytes) }))
        setCapDirty(false)
      },
      onError: (err: Error & { response?: { data?: { message?: string } } }) =>
        toast.error(err.response?.data?.message || t('artifactCache.capFailed')),
    })
  }

  const items = data?.items ?? []
  const totalBytes = data?.totalBytes ?? 0
  const capBytes = data?.capBytes ?? 0
  const usagePct = capBytes > 0 ? Math.min(100, (totalBytes / capBytes) * 100) : 0
  const filteredItems = items.filter((it) => {
    const q = query.trim().toLowerCase()
    if (!q) return true
    return `${it.name} ${it.version} ${it.sha256}`.toLowerCase().includes(q)
  })

  return (
    <div className="space-y-3">
      {/* 头部：容量条（占用/上限）+ 上限设置 + 一键清空（常驻稳定区，布局不跳） */}
      <div className="space-y-2 rounded-md border bg-card px-3 py-2.5">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div className="min-w-[200px] flex-1">
            <div className="mb-1.5 flex items-baseline gap-2">
              <Database className="size-4 self-center text-muted-foreground" />
              <span className="text-sm text-muted-foreground">{t('artifactCache.total')}</span>
              <span className="font-mono text-base font-medium">{formatCacheBytes(totalBytes)}</span>
              <span className="text-xs text-muted-foreground">/ {describeCap(capBytes)}</span>
            </div>
            {capBytes > 0 && (
              <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                <div className="h-full rounded-full bg-primary transition-all" style={{ width: `${usagePct}%` }} />
              </div>
            )}
          </div>
          <div className="flex items-center gap-2">
          <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
            {t('artifactCache.capInputLabel')}
            <Input
              value={capValue}
              onChange={(e) => { setCapInput(e.target.value); setCapDirty(true) }}
              inputMode="decimal"
              placeholder="0"
              className="h-7 w-20 text-sm"
              aria-label={t('artifactCache.capInputLabel')}
            />
          </label>
          <Button size="sm" variant="outline" onClick={onSaveCap} disabled={setCap.isPending || !capDirty}>
            {t('common.save')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setConfirmClear(true)}
            disabled={clear.isPending || items.length === 0}
          >
            <Trash2 className="size-3.5" />
            {t('artifactCache.clearAll')}
          </Button>
          </div>
        </div>
      </div>

      {/* 列表：搜索 + 行卡片（FR-195） */}
      {isLoading ? (
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-sm text-destructive">{t('artifactCache.loadFailed')}</p>
      ) : items.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{t('artifactCache.empty')}</p>
      ) : (
        <div className="space-y-2">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('artifactCache.searchPlaceholder')}
              className="h-9 pl-8"
            />
          </div>
          {filteredItems.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">{t('artifactCache.empty')}</p>
          ) : (
            filteredItems.map((it) => (
              <div
                key={it.sha256}
                className="flex items-center gap-3 rounded-lg border bg-card px-3 py-2.5 transition-colors hover:bg-muted/40"
              >
                <div className="flex size-9 shrink-0 items-center justify-center rounded-md bg-accent text-primary">
                  <FileArchive className="size-[18px]" />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium">{it.name || '—'}</span>
                    {it.version && <span className="shrink-0 text-xs text-muted-foreground">{it.version}</span>}
                  </div>
                  <button
                    type="button"
                    className="mt-0.5 flex items-center gap-1 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
                    title={it.sha256}
                    onClick={async () => {
                      const ok = await copyToClipboard(it.sha256)
                      if (ok) toast.success(t('artifactCache.shaCopied'))
                      else toast.error(t('common.copyFailed'))
                    }}
                  >
                    <span>{it.sha256.slice(0, 12)}…</span>
                    <Copy className="size-3" />
                  </button>
                </div>
                <div className="shrink-0 text-right">
                  <div className="font-mono text-sm font-medium">{formatCacheBytes(it.size)}</div>
                  <div className="text-[11px] text-muted-foreground">{fmtTime(it.lastUsedAt)}</div>
                </div>
                <button
                  type="button"
                  aria-label={t('artifactCache.evict')}
                  className="shrink-0 text-muted-foreground transition-colors hover:text-status-danger"
                  onClick={() => setPendingEvict(it)}
                >
                  <Trash2 className="size-4" />
                </button>
              </div>
            ))
          )}
        </div>
      )}

      <DangerConfirm
        open={pendingEvict !== null}
        title={t('artifactCache.evictTitle')}
        description={t('artifactCache.evictDesc', { name: pendingEvict?.name || pendingEvict?.sha256.slice(0, 12) })}
        confirmLabel={t('artifactCache.evict')}
        onConfirm={() => {
          const sha = pendingEvict!.sha256
          setPendingEvict(null)
          evict.mutate(sha, {
            onSuccess: () => toast.success(t('artifactCache.evicted')),
            onError: (err: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(err.response?.data?.message || t('artifactCache.evictFailed')),
          })
        }}
        onCancel={() => setPendingEvict(null)}
      />

      <DangerConfirm
        open={confirmClear}
        title={t('artifactCache.clearTitle')}
        description={t('artifactCache.clearDesc')}
        confirmLabel={t('artifactCache.clearAll')}
        onConfirm={() => {
          setConfirmClear(false)
          clear.mutate(undefined, {
            onSuccess: () => toast.success(t('artifactCache.cleared')),
            onError: (err: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(err.response?.data?.message || t('artifactCache.clearFailed')),
          })
        }}
        onCancel={() => setConfirmClear(false)}
      />
    </div>
  )
}
