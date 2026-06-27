import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Trash2, Copy, Database } from 'lucide-react'
import {
  useArtifactCache,
  useEvictArtifactCache,
  useClearArtifactCache,
  useSetArtifactCacheCap,
  type ArtifactCacheItem,
} from '@/api/nodeRuntime'
import { formatCacheBytes, capGiBToBytes, capBytesToGiB, describeCap } from '@/lib/artifact-cache'
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

  return (
    <div className="space-y-3">
      {/* 头部：总占用 + 上限设置（常驻稳定区，布局不跳） */}
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border bg-muted/30 px-3 py-2">
        <div className="flex items-center gap-2 text-sm">
          <Database className="size-4 text-muted-foreground" />
          <span className="font-medium">{t('artifactCache.total')}</span>
          <span className="font-mono">{formatCacheBytes(data?.totalBytes ?? 0)}</span>
          <span className="text-muted-foreground">·</span>
          <span className="text-muted-foreground">
            {t('artifactCache.capLabel')}: {describeCap(data?.capBytes ?? 0)}
          </span>
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

      {/* 列表 */}
      {isLoading ? (
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-sm text-destructive">{t('artifactCache.loadFailed')}</p>
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('artifactCache.empty')}</p>
      ) : (
        <div className="overflow-x-auto rounded-md border">
          <table className="w-full text-sm">
            <thead className="bg-muted">
              <tr>
                <th className="px-2 py-1.5 text-left font-medium">{t('artifactCache.colName')}</th>
                <th className="px-2 py-1.5 text-left font-medium">{t('artifactCache.colVersion')}</th>
                <th className="px-2 py-1.5 text-right font-medium">{t('artifactCache.colSize')}</th>
                <th className="px-2 py-1.5 text-left font-medium whitespace-nowrap">{t('artifactCache.colLastUsed')}</th>
                <th className="px-2 py-1.5 text-left font-medium">{t('artifactCache.colSha')}</th>
                <th className="px-2 py-1.5 text-right font-medium">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {items.map((it) => (
                <tr key={it.sha256} className="border-t">
                  <td className="px-2 py-1.5">{it.name || '—'}</td>
                  <td className="px-2 py-1.5">{it.version || '—'}</td>
                  <td className="px-2 py-1.5 text-right font-mono">{formatCacheBytes(it.size)}</td>
                  <td className="px-2 py-1.5 whitespace-nowrap text-xs text-muted-foreground">{fmtTime(it.lastUsedAt)}</td>
                  <td className="px-2 py-1.5">
                    <button
                      type="button"
                      className="flex items-center gap-1 font-mono text-xs text-muted-foreground hover:text-foreground"
                      title={it.sha256}
                      onClick={() => {
                        navigator.clipboard?.writeText(it.sha256).then(
                          () => toast.success(t('artifactCache.shaCopied')),
                          () => toast.error(t('common.copyFailed')),
                        )
                      }}
                    >
                      <span>{it.sha256.slice(0, 12)}…</span>
                      <Copy className="size-3" />
                    </button>
                  </td>
                  <td className="px-2 py-1.5 text-right">
                    <button
                      type="button"
                      className="text-xs text-destructive hover:underline"
                      onClick={() => setPendingEvict(it)}
                    >
                      {t('artifactCache.evict')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
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
