import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Trash2, Copy, Database, FileArchive, Search } from 'lucide-react'
import { copyToClipboard } from '@/lib/shared/clipboard'
import { formatCacheBytes, capGiBToBytes, capBytesToGiB, describeCap } from '@/lib/artifacts/artifact-cache'
import type { ArtifactCacheItem, ArtifactCacheView } from '@/lib/artifacts/artifact-cache'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import DangerConfirm from '@/components/views/DangerConfirm'

/** 把 Unix 秒格式化为本地日期时间；0/空回「—」。 */
function fmtTime(sec: number): string {
  if (!sec) return '—'
  return new Date(sec * 1000).toLocaleString()
}

/**
 * 节点制品缓存面板（FR-178）：列缓存项（名/版本/大小/最近用）+ 总占用 + 容量上限设置 + 清/逐项清。
 *
 * 真·节点级（性能优化）：Worker 按 sha256 缓存下载过的核心 jar，建实例命中即秒拷免重下。
 * 全局制品库管理仍归控制面板（FR-082），此面板只看/清这份本地缓存。
 *
 * 受控视图（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**——
 * - 数据与加载/错误态经 props 注入（外壳调 `@/api/nodeRuntime` 的查询 hook）；
 * - 三个写动作以回调上报，由外壳执行 mutation 并决定成功/失败文案与提示；
 *   回调返回 Promise<boolean>，组件据此决定是否复位本地编辑态（失败时保留输入便于重试）；
 * - 二次确认弹窗、搜索词、上限输入框等 UI 状态留在本组件内。
 */
export interface NodeArtifactCachePanelProps {
  /** 缓存视图（列表 + 总占用 + 上限）；外壳取数后注入。 */
  data?: ArtifactCacheView
  /** 加载态；外壳注入。 */
  isLoading?: boolean
  /** 错误态；外壳注入。 */
  isError?: boolean
  /** 保存容量上限（参数为字节数，0=不限）。返回是否成功。 */
  onSaveCap: (bytes: number) => Promise<boolean>
  /** 逐项清理。返回是否成功。 */
  onEvict: (sha256: string) => Promise<boolean>
  /** 清空全部缓存。返回是否成功。 */
  onClear: () => Promise<boolean>
  /** 复制 sha256 后的结果上报（由外壳决定提示文案）。 */
  onCopyResult?: (ok: boolean) => void
  /** 保存上限在途：禁用保存按钮。 */
  capSaving?: boolean
  /** 清空在途：禁用清空按钮。 */
  clearing?: boolean
}

export default function NodeArtifactCachePanel({
  data,
  isLoading,
  isError,
  onSaveCap,
  onEvict,
  onClear,
  onCopyResult,
  capSaving = false,
  clearing = false,
}: NodeArtifactCachePanelProps) {
  const { t } = useTranslation()

  const [capInput, setCapInput] = useState('')
  const [capDirty, setCapDirty] = useState(false)
  const [pendingEvict, setPendingEvict] = useState<ArtifactCacheItem | null>(null)
  const [confirmClear, setConfirmClear] = useState(false)
  const [query, setQuery] = useState('')

  // 上限输入：未编辑时回显服务端值（GB）；编辑后用本地值（稳定区，不切换隐显）。
  const capValue = capDirty ? capInput : capBytesToGiB(data?.capBytes ?? 0)

  const handleSaveCap = async () => {
    const bytes = capGiBToBytes(capValue)
    const ok = await onSaveCap(bytes)
    // 成功后退回回显态（显示服务端已生效的值）；失败保留输入，便于用户修正后重试。
    if (ok) setCapDirty(false)
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
          <Button size="sm" variant="outline" onClick={handleSaveCap} disabled={capSaving || !capDirty}>
            {t('common.save')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setConfirmClear(true)}
            disabled={clearing || items.length === 0}
          >
            <Trash2 className="size-3.5" />
            {t('artifactCache.clearAll')}
          </Button>
          </div>
        </div>
      </div>

      {/* 列表：搜索 + 行卡片（FR-195） */}
      {isLoading ? (
        /* 骨架占位：按行卡片（size-9 图标 + 双行文本 ≈ h-14）轮廓，替代裸文字。 */
        <div className="space-y-2">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
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
                    {/* 类型徽章（FR-330）：核心缓存条目一眼可辨（当前仅 core 一类，按类型渐进扩展）。 */}
                    {it.type === 'core' && (
                      <span className="shrink-0 rounded-full bg-accent px-2 py-0.5 text-[10px] font-medium text-primary">
                        {t('artifactCache.typeCore')}
                      </span>
                    )}
                    {it.version && <span className="shrink-0 text-xs text-muted-foreground">{it.version}</span>}
                  </div>
                  <button
                    type="button"
                    className="mt-0.5 flex items-center gap-1 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
                    title={it.sha256}
                    onClick={async () => {
                      const ok = await copyToClipboard(it.sha256)
                      onCopyResult?.(ok)
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
          void onEvict(sha)
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
          void onClear()
        }}
        onCancel={() => setConfirmClear(false)}
      />
    </div>
  )
}
