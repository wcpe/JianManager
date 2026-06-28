import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  ArrowRight,
  Ban,
  Check,
  ChevronLeft,
  Copy,
  DownloadCloud,
  KeyRound,
  Plus,
  RefreshCw,
} from 'lucide-react'
import {
  useClientChannels,
  useClientChannel,
  useCreateClientChannel,
  useDeleteClientChannel,
  useCreateClientKey,
  useRotateClientKey,
  useRevokeClientKey,
  type ClientChannel,
  type ClientPullKey,
  type ClientKeyWithSecret,
} from '@/api/clientChannels'
import { copyToClipboard } from '@/lib/clipboard'
import {
  deriveReadiness,
  readinessCompletedCount,
  type ReadinessStep,
  type ReadinessStepId,
} from '@/lib/client-readiness'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import DangerConfirm from '@/components/DangerConfirm'
import ClientVersionsPanel from '@/components/ClientVersionsPanel'
import ClientStatsPanel from '@/components/ClientStatsPanel'
import ClientIntegrationGuide from '@/components/ClientIntegrationGuide'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/** 工作台分段标识，与就绪度步骤 CTA 联动跳转。 */
type WorkbenchTab = 'keys' | 'versions' | 'stats' | 'guide'

/**
 * 客户端分发管理页（FR-086/187，见 ADR-022）。
 * 运营域入口（FR-187 由「系统·平台与维护」迁入，路由 /client-channels 不变）。
 * 频道（每服一个）+ 拉取密钥（落库只存哈希、明文一次性返回）；首次使用以空状态引导卡 +
 * 工作台就绪度步骤器降低门槛。仅平台管理员可用（后端 RBAC 强制）。
 * i18n（FR-016）+ 暗/亮色（FR-026，全程用主题 token）。
 */
export default function ClientChannelsPage() {
  const { t } = useTranslation()
  const { data: channels, isLoading } = useClientChannels()
  const [selected, setSelected] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)

  if (selected) {
    return <ChannelWorkbench channelId={selected} onBack={() => setSelected(null)} />
  }

  const list = channels ?? []
  const isEmpty = list.length === 0 && !isLoading

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-3 flex-wrap">
        <div>
          <h1 className="text-2xl font-bold flex items-center gap-2">
            <DownloadCloud className="size-6" /> {t('clientChannels.title', '客户端分发')}
          </h1>
          <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
            {t('clientChannels.subtitle', '管理客户端分发频道与拉取密钥。每服一个频道，密钥用于玩家侧更新器拉取。')}
          </p>
        </div>
        {!isEmpty && (
          <Button onClick={() => setCreateOpen(true)} className="shrink-0">
            <Plus className="size-4" /> {t('clientChannels.addChannel', '新增频道')}
          </Button>
        )}
      </div>

      {isEmpty ? (
        <EmptyChannelsGuide onCreate={() => setCreateOpen(true)} />
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {list.map((ch: ClientChannel) => (
            <ChannelCard key={ch.id} channel={ch} onOpen={() => setSelected(ch.channelId)} />
          ))}
        </div>
      )}

      <CreateChannelDialog open={createOpen} onOpenChange={setCreateOpen} onCreated={(id) => setSelected(id)} />
    </div>
  )
}

/** 空状态大引导卡：说明用途 + 主 CTA「创建第一个分发频道」。 */
function EmptyChannelsGuide({ onCreate }: { onCreate: () => void }) {
  const { t } = useTranslation()
  return (
    <div className="rounded-xl border border-dashed bg-card/40 p-10 text-center flex flex-col items-center gap-4">
      <span className="grid size-14 place-items-center rounded-full bg-primary/10 text-primary">
        <DownloadCloud className="size-7" />
      </span>
      <div className="space-y-1 max-w-md">
        <h2 className="text-lg font-semibold">{t('clientChannels.emptyTitle', '创建第一个分发频道')}</h2>
        <p className="text-sm text-muted-foreground">
          {t(
            'clientChannels.emptyDesc',
            '分发频道是玩家客户端 OTA 更新的入口：建频道 → 拉取密钥 → 发布版本 → 接入启动器，四步即可让玩家自动收到更新。',
          )}
        </p>
      </div>
      <Button onClick={onCreate} size="lg">
        <Plus className="size-4" /> {t('clientChannels.createFirst', '创建分发频道')}
      </Button>
    </div>
  )
}

/** 频道卡片：当前版本 / 密钥数 + 就绪度小标，点击进入工作台。 */
function ChannelCard({ channel, onOpen }: { channel: ClientChannel; onOpen: () => void }) {
  const { t } = useTranslation()
  const steps = useMemo(
    () => deriveReadiness({ keyCount: channel.keyCount ?? 0, currentVersion: channel.currentVersion }),
    [channel.keyCount, channel.currentVersion],
  )
  const completed = readinessCompletedCount(steps)
  const ready = completed === steps.length

  return (
    <button
      type="button"
      onClick={onOpen}
      className="group text-left rounded-xl border bg-card/40 p-4 transition-colors hover:border-primary/40 hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="font-semibold truncate">{channel.name}</div>
          <div className="font-mono text-xs text-muted-foreground truncate">{channel.channelId}</div>
        </div>
        <ArrowRight className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
      </div>

      {channel.description && (
        <p className="mt-2 text-xs text-muted-foreground line-clamp-2">{channel.description}</p>
      )}

      <div className="mt-3 flex items-center gap-2 flex-wrap text-xs">
        <Badge variant={channel.currentVersion > 0 ? 'default' : 'outline'}>
          {channel.currentVersion > 0
            ? `v${channel.currentVersion}`
            : t('clientChannels.unpublished', '未发布')}
        </Badge>
        <Badge variant="outline">
          {t('clientChannels.keyCountBadge', '{{n}} 个密钥', { n: channel.keyCount ?? 0 })}
        </Badge>
        {ready ? (
          <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-500">
            <Check className="size-3.5" /> {t('clientChannels.ready', '已就绪')}
          </span>
        ) : (
          <span className="text-muted-foreground">
            {t('clientChannels.readinessShort', '就绪度 {{c}}/{{n}}', { c: completed, n: steps.length })}
          </span>
        )}
      </div>
    </button>
  )
}

/** 创建频道模态（FR-187，取代原内联展开表单；内容自适应壳）。 */
function CreateChannelDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (channelId: string) => void
}) {
  const { t } = useTranslation()
  const create = useCreateClientChannel()
  const [channelId, setChannelId] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  const slugOk = /^[a-z0-9][a-z0-9-]{1,63}$/.test(channelId)
  const canSubmit = slugOk && name.trim() !== '' && !create.isPending

  const reset = () => {
    setChannelId('')
    setName('')
    setDescription('')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      await create.mutateAsync({ channelId, name, description })
      toast.success(t('clientChannels.created', '频道已创建'))
      const created = channelId
      reset()
      onOpenChange(false)
      onCreated(created)
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.createFailed', '创建频道失败')))
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v: boolean) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-lg')}>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.addChannel', '新增频道')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.createDialogDesc', '为一个服务器创建分发频道；创建后进入工作台继续配置密钥与发布版本。')}
          </DialogDescription>
        </DialogHeader>
        <form id="create-channel-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.channelId', '频道标识')}
              <input
                className="p-2 border rounded bg-background font-mono aria-invalid:border-destructive"
                placeholder="skyblock-s1"
                aria-invalid={channelId !== '' && !slugOk}
                value={channelId}
                onChange={(e) => setChannelId(e.target.value)}
                autoFocus
              />
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.channelIdHint', '小写字母/数字/连字符，2-64 位，创建后不可改')}
              </span>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('common.name', '名称')}
              <input
                className="p-2 border rounded bg-background"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.description', '描述')}
              <input
                className="p-2 border rounded bg-background"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </label>
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="create-channel-form" disabled={!canSubmit}>
            {t('common.create', '创建')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * 频道工作台：顶部就绪度步骤器（状态由 keyCount/currentVersion 推导）+
 * 密钥 / 版本 / 统计 / 接入指引 分段。取代原 ChannelDetail，全程模态化。
 */
function ChannelWorkbench({ channelId, onBack }: { channelId: string; onBack: () => void }) {
  const { t } = useTranslation()
  const { data: detail, isLoading } = useClientChannel(channelId)
  const del = useDeleteClientChannel()

  const [tab, setTab] = useState<WorkbenchTab>('keys')
  const [deleteChannel, setDeleteChannel] = useState(false)
  // 就绪度步骤器「创建密钥」CTA 直接开建密钥模态（BUG-E）：开关上提到工作台、随 tab 自动归零。
  const [keyCreateOpen, setKeyCreateOpen] = useState(false)

  const keyCount = detail?.keys?.length ?? 0
  const steps = useMemo(
    () => deriveReadiness({ keyCount, currentVersion: detail?.currentVersion ?? 0 }),
    [keyCount, detail?.currentVersion],
  )

  const doDeleteChannel = () => {
    setDeleteChannel(false)
    del.mutate(channelId, {
      onSuccess: () => {
        toast.success(t('clientChannels.channelDeleted', '频道已删除'))
        onBack()
      },
      onError: (e) => toast.error(errMsg(e, t('clientChannels.deleteFailed', '删除频道失败'))),
    })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div className="flex items-center gap-2">
          <button
            className="text-muted-foreground hover:text-foreground"
            onClick={onBack}
            aria-label={t('common.back', '返回')}
          >
            <ChevronLeft className="size-5" />
          </button>
          <div>
            <h1 className="text-2xl font-bold flex items-center gap-2">
              <KeyRound className="size-5" /> {detail?.name ?? channelId}
            </h1>
            <p className="text-xs text-muted-foreground font-mono mt-1">{channelId}</p>
          </div>
        </div>
        <button
          className="text-destructive hover:underline text-sm"
          onClick={() => setDeleteChannel(true)}
        >
          {t('clientChannels.deleteChannel', '删除频道')}
        </button>
      </div>

      <ReadinessStepper
        steps={steps}
        onCta={(id) => {
          setTab(STEP_META[id].goto)
          if (id === 'keys') setKeyCreateOpen(true)
        }}
      />

      <Tabs value={tab} onValueChange={(v) => setTab(v as WorkbenchTab)}>
        <TabsList variant="line">
          <TabsTrigger value="keys">{t('clientChannels.manageKeys', '拉取密钥')}</TabsTrigger>
          <TabsTrigger value="versions">{t('clientVersions.tab', '版本管理')}</TabsTrigger>
          <TabsTrigger value="stats">{t('clientStats.tab', '统计')}</TabsTrigger>
          <TabsTrigger value="guide">{t('clientGuide.tab', '接入指引')}</TabsTrigger>
        </TabsList>
        <TabsContent value="keys">
          <KeysSegment
            channelId={channelId}
            keys={detail?.keys ?? []}
            loading={isLoading}
            createOpen={keyCreateOpen && tab === 'keys'}
            onCreateOpenChange={setKeyCreateOpen}
          />
        </TabsContent>
        <TabsContent value="versions">
          <ClientVersionsPanel channelId={channelId} />
        </TabsContent>
        <TabsContent value="stats">
          <ClientStatsPanel channelId={channelId} />
        </TabsContent>
        <TabsContent value="guide">
          <ClientIntegrationGuide channelId={channelId} />
        </TabsContent>
      </Tabs>

      <DangerConfirm
        open={deleteChannel}
        title={t('clientChannels.deleteChannelConfirm', '确定删除此频道？')}
        description={t('clientChannels.deleteChannelDesc', '将连同其全部拉取密钥一并删除，不可恢复。')}
        scope="platform"
        confirmText={channelId}
        confirmLabel={t('common.delete', '删除')}
        onConfirm={doDeleteChannel}
        onCancel={() => setDeleteChannel(false)}
      />
    </div>
  )
}

/** 步骤器文案配置（标题 + 引导说明 + CTA 跳转 tab）。 */
const STEP_META: Record<
  ReadinessStepId,
  { titleKey: string; titleFallback: string; hintKey: string; hintFallback: string; ctaKey: string; ctaFallback: string; goto: WorkbenchTab }
> = {
  channel: {
    titleKey: 'clientChannels.stepChannel',
    titleFallback: '创建频道',
    hintKey: 'clientChannels.stepChannelHint',
    hintFallback: '频道已创建。',
    ctaKey: '',
    ctaFallback: '',
    goto: 'keys',
  },
  keys: {
    titleKey: 'clientChannels.stepKeys',
    titleFallback: '拉取密钥',
    hintKey: 'clientChannels.stepKeysHint',
    hintFallback: '创建一个拉取密钥，供玩家侧更新器鉴权拉取。',
    ctaKey: 'clientChannels.createKey',
    ctaFallback: '创建密钥',
    goto: 'keys',
  },
  version: {
    titleKey: 'clientChannels.stepVersion',
    titleFallback: '发布版本',
    hintKey: 'clientChannels.stepVersionHint',
    hintFallback: '上传客户端文件并发布第一个版本，玩家即可拉取 latest。',
    ctaKey: 'clientVersions.publish',
    ctaFallback: '发布新版本',
    goto: 'versions',
  },
  integrate: {
    titleKey: 'clientChannels.stepIntegrate',
    titleFallback: '接入启动器',
    hintKey: 'clientChannels.stepIntegrateHint',
    hintFallback: '照接入指引把更新器接入整合包并下发玩家。',
    ctaKey: 'clientChannels.viewGuide',
    ctaFallback: '查看接入指引',
    goto: 'guide',
  },
}

/** 顶部常驻就绪度步骤器（纯展示，状态由 detail 推导）。 */
function ReadinessStepper({ steps, onCta }: { steps: ReadinessStep[]; onCta: (stepId: ReadinessStepId) => void }) {
  const { t } = useTranslation()
  const current = steps.find((s) => s.current)
  const completed = readinessCompletedCount(steps)
  const allReady = completed === steps.length

  return (
    <div className="rounded-xl border bg-card/40 p-4 space-y-3">
      <div className="flex items-center justify-between gap-2 flex-wrap">
        <h2 className="text-sm font-semibold">{t('clientChannels.readinessTitle', '接入就绪度')}</h2>
        <span className="text-xs text-muted-foreground">
          {allReady
            ? t('clientChannels.readyAll', '全部就绪，玩家可正常更新')
            : t('clientChannels.readinessShort', '就绪度 {{c}}/{{n}}', { c: completed, n: steps.length })}
        </span>
      </div>

      <ol className="flex items-stretch gap-2 flex-wrap">
        {steps.map((s, i) => {
          const meta = STEP_META[s.id]
          return (
            <li key={s.id} className="flex items-center gap-2">
              <div
                className={cn(
                  'flex items-center gap-2 rounded-lg border px-3 py-2 text-sm',
                  s.current && 'border-primary/50 bg-primary/5',
                  s.done && !s.current && 'border-emerald-500/30 bg-emerald-500/5',
                )}
              >
                <span
                  className={cn(
                    'grid size-6 shrink-0 place-items-center rounded-full text-xs font-medium',
                    s.done
                      ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-500'
                      : s.current
                        ? 'bg-primary text-primary-foreground'
                        : 'bg-muted text-muted-foreground',
                  )}
                >
                  {s.done ? <Check className="size-3.5" /> : i + 1}
                </span>
                <span className={cn('whitespace-nowrap', s.done && !s.current && 'text-muted-foreground')}>
                  {t(meta.titleKey, meta.titleFallback)}
                </span>
              </div>
              {i < steps.length - 1 && <ArrowRight className="size-3.5 shrink-0 text-muted-foreground/50" />}
            </li>
          )
        })}
      </ol>

      {current && (
        <div className="flex items-center justify-between gap-3 flex-wrap rounded-lg bg-muted/40 px-3 py-2">
          <p className="text-xs text-muted-foreground">{t(STEP_META[current.id].hintKey, STEP_META[current.id].hintFallback)}</p>
          {STEP_META[current.id].ctaKey && (
            <Button size="sm" variant="outline" className="shrink-0" onClick={() => onCta(current.id)}>
              {t(STEP_META[current.id].ctaKey, STEP_META[current.id].ctaFallback)}
              <ArrowRight className="size-3.5" />
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

/** 密钥分段：列表 + 「创建密钥」模态 + 轮换/吊销（DangerConfirm）+ 一次性明文弹窗。 */
function KeysSegment({
  channelId,
  keys,
  loading,
  createOpen,
  onCreateOpenChange,
}: {
  channelId: string
  keys: ClientPullKey[]
  loading: boolean
  createOpen: boolean
  onCreateOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation()
  const rotateKey = useRotateClientKey()
  const revokeKey = useRevokeClientKey()

  const [secret, setSecret] = useState<ClientKeyWithSecret | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ClientPullKey | null>(null)
  const [rotateTarget, setRotateTarget] = useState<ClientPullKey | null>(null)

  const doRotate = async (key: ClientPullKey) => {
    setRotateTarget(null)
    try {
      const res = await rotateKey.mutateAsync({ channelId, keyId: key.id })
      setSecret(res)
      toast.success(t('clientChannels.rotated', '密钥已轮换'))
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.rotateFailed', '轮换密钥失败')))
    }
  }

  const doRevoke = (key: ClientPullKey) => {
    setRevokeTarget(null)
    revokeKey.mutate(
      { channelId, keyId: key.id },
      {
        onSuccess: () => toast.success(t('clientChannels.revoked', '密钥已吊销')),
        onError: (e) => toast.error(errMsg(e, t('clientChannels.revokeFailed', '吊销密钥失败'))),
      },
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t('clientChannels.keysSubtitle', '拉取密钥落库只存哈希，明文仅创建/轮换时一次性显示。请妥善保存到 jm-updater.json。')}
        </p>
        <Button onClick={() => onCreateOpenChange(true)} className="shrink-0">
          <Plus className="size-4" /> {t('clientChannels.createKey', '创建密钥')}
        </Button>
      </div>

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted">
            <tr>
              <th className="p-3 text-left">{t('common.name', '名称')}</th>
              <th className="p-3 text-left">{t('clientChannels.keyPrefix', '前缀')}</th>
              <th className="p-3 text-left">{t('common.status', '状态')}</th>
              <th className="p-3 text-left">{t('clientChannels.expiresAt', '过期时间')}</th>
              <th className="p-3 text-left">{t('clientChannels.lastUsed', '最近使用')}</th>
              <th className="p-3 text-left">{t('common.actions', '操作')}</th>
            </tr>
          </thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k.id} className="border-t">
                <td className="p-3">{k.name}</td>
                <td className="p-3 font-mono text-xs">{k.keyPrefix}…</td>
                <td className="p-3">
                  {k.revoked ? (
                    <Badge variant="outline" className="text-destructive border-destructive/40">
                      {t('clientChannels.statusRevoked', '已吊销')}
                    </Badge>
                  ) : (
                    <Badge variant="outline">{t('clientChannels.statusActive', '有效')}</Badge>
                  )}
                </td>
                <td className="p-3 text-xs">{k.expiresAt ? new Date(k.expiresAt).toLocaleString() : '-'}</td>
                <td className="p-3 text-xs">{k.lastUsedAt ? new Date(k.lastUsedAt).toLocaleString() : '-'}</td>
                <td className="p-3">
                  <div className="flex gap-3">
                    <button
                      className="text-primary hover:underline inline-flex items-center gap-1 disabled:opacity-40"
                      onClick={() => setRotateTarget(k)}
                      disabled={k.revoked}
                    >
                      <RefreshCw className="size-3.5" /> {t('clientChannels.rotate', '轮换')}
                    </button>
                    <button
                      className="text-destructive hover:underline inline-flex items-center gap-1 disabled:opacity-40"
                      onClick={() => setRevokeTarget(k)}
                      disabled={k.revoked}
                    >
                      <Ban className="size-3.5" /> {t('clientChannels.revoke', '吊销')}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {keys.length === 0 && !loading && (
              <tr>
                <td colSpan={6} className="p-3 text-center text-muted-foreground">
                  {t('clientChannels.noKeys', '暂无密钥')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <CreateKeyDialog
        channelId={channelId}
        open={createOpen}
        onOpenChange={onCreateOpenChange}
        onCreated={(s) => setSecret(s)}
      />

      <SecretDialog secret={secret} onClose={() => setSecret(null)} />

      <DangerConfirm
        open={rotateTarget !== null}
        title={t('clientChannels.rotateConfirm', '确定轮换此密钥？')}
        description={t('clientChannels.rotateConfirmDesc', '轮换后旧密钥立即失效，已分发的客户端需更新为新密钥。')}
        scope="platform"
        confirmLabel={t('clientChannels.rotate', '轮换')}
        onConfirm={() => rotateTarget && doRotate(rotateTarget)}
        onCancel={() => setRotateTarget(null)}
      />

      <DangerConfirm
        open={revokeTarget !== null}
        title={t('clientChannels.revokeConfirm', '确定吊销此密钥？')}
        description={t('clientChannels.revokeConfirmDesc', '吊销后使用此密钥的客户端将无法再拉取更新。')}
        scope="platform"
        confirmLabel={t('clientChannels.revoke', '吊销')}
        onConfirm={() => revokeTarget && doRevoke(revokeTarget)}
        onCancel={() => setRevokeTarget(null)}
      />
    </div>
  )
}

/** 创建拉取密钥模态（FR-187，取代原内联常驻表单；内容自适应壳）。 */
function CreateKeyDialog({
  channelId,
  open,
  onOpenChange,
  onCreated,
}: {
  channelId: string
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (secret: ClientKeyWithSecret) => void
}) {
  const { t } = useTranslation()
  const createKey = useCreateClientKey()
  const [keyName, setKeyName] = useState('')
  const [expiresAt, setExpiresAt] = useState('')

  const canSubmit = keyName.trim() !== '' && !createKey.isPending

  const reset = () => {
    setKeyName('')
    setExpiresAt('')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      const res = await createKey.mutateAsync({
        channelId,
        name: keyName,
        expiresAt: expiresAt ? new Date(expiresAt).toISOString() : undefined,
      })
      reset()
      onOpenChange(false)
      onCreated(res)
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.keyCreateFailed', '创建密钥失败')))
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v: boolean) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-md')}>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.createKey', '创建密钥')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.createKeyDesc', '创建后明文密钥仅显示一次，请立即复制保存。')}
          </DialogDescription>
        </DialogHeader>
        <form id="create-key-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyName', '密钥名称')}
              <input
                className="p-2 border rounded bg-background"
                placeholder={t('clientChannels.keyNamePlaceholder', '如：正式包 / 灰度')}
                value={keyName}
                onChange={(e) => setKeyName(e.target.value)}
                autoFocus
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.expiresAt', '过期时间（可选）')}
              <input
                type="datetime-local"
                className="p-2 border rounded bg-background"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
              />
            </label>
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="create-key-form" disabled={!canSubmit}>
            {t('clientChannels.createKey', '创建密钥')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 一次性明文展示弹窗：仅创建/轮换后可见，提示复制保存（复制走 copyToClipboard 兜底 HTTP 非安全上下文）。 */
function SecretDialog({ secret, onClose }: { secret: ClientKeyWithSecret | null; onClose: () => void }) {
  const { t } = useTranslation()

  const copy = async () => {
    if (!secret) return
    const ok = await copyToClipboard(secret.key)
    if (ok) toast.success(t('clientChannels.copied', '已复制到剪贴板'))
    else toast.error(t('clientChannels.copyFailed', '复制失败，请手动选择复制'))
  }

  return (
    <Dialog open={secret !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.secretTitle', '拉取密钥（仅此一次）')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.secretDesc', '请立即复制保存。关闭后将无法再次查看完整密钥。')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 rounded-md border bg-muted/50 p-3">
          <code className="flex-1 break-all font-mono text-sm">{secret?.key}</code>
          <Button variant="outline" size="sm" onClick={copy} className="shrink-0">
            <Copy className="size-4" /> {t('clientChannels.copy', '复制')}
          </Button>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{t('common.close', '关闭')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
