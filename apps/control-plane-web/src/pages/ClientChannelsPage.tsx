import { useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ArrowLeft, Plus } from 'lucide-react'
import { useClientChannels, useClientChannel, useCreateClientChannel, useDeleteClientChannel, useCreateClientKey, useUpdateClientKey, useRevokeClientKey, useRevealClientKey, type ClientChannel } from '@/api/clientChannels'
import { useClientChannelSecuritySummary, type ClientChannelSecuritySummary } from '@/api/clientDistSecurity'
import { buildClientDistHref, readClientDistQuery, updateClientDistQuery } from '@/lib/client-dist/client-dist-query'
import { useTabParam } from '@/lib/hooks/use-tab-param'
import { deriveReadiness } from '@/lib/client-dist/client-readiness'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { ObjectPageHeader } from '@jianmanager/ui/components/shell'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'
import DangerConfirm from '@/components/common/DangerConfirm'
import ClientVersionsPanel from '@/components/client-dist/ClientVersionsPanel'
import ClientStatsPanel from '@/components/client-dist/ClientStatsPanel'
import ClientIntegrationGuide from '@/components/client-dist/ClientIntegrationGuide'
import ClientUpdaterCoreSelector from '@/components/client-dist/ClientUpdaterCoreSelector'
import ClientDistFlowGuide from '@/components/views/ClientDistFlowGuide'
import { ReadinessStepper, STEP_META } from '@/components/views/client-dist/ReadinessStepper'
import { ChannelCard, EmptyChannelsGuide } from '@/components/views/client-dist/ChannelCards'
import { CreateChannelDialog } from '@/components/views/client-dist/CreateChannelDialog'
import { KeysSegment } from '@/components/views/client-dist/KeysSegment'
import { ChannelSecuritySummaryBar } from '@/components/views/client-dist/ChannelSecuritySummaryBar'
import { useDangerPermission } from '@/lib/shared/danger'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/** 工作台分段标识，与就绪度步骤 CTA 联动跳转。 */
type WorkbenchTab = 'keys' | 'versions' | 'core' | 'stats' | 'guide'

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
  const createChannel = useCreateClientChannel()
  const [searchParams, setSearchParams] = useSearchParams()
  // 兼容历史 `channel`，统一按 `channelId` 还原频道工作台。
  const selected = readClientDistQuery(searchParams).channelId ?? null
  const [createOpen, setCreateOpen] = useState(false)

  useEffect(() => {
    const channelId = readClientDistQuery(searchParams).channelId
    if (channelId && searchParams.has('channel') && !searchParams.has('channelId')) {
      setSearchParams(updateClientDistQuery(searchParams, { channelId }), { replace: true })
    }
  }, [searchParams, setSearchParams])

  /** 返回频道列表：清状态与 URL 参数（避免刷新后又自动展开工作台）。 */
  const backToList = () => {
    if (searchParams.has('channel') || searchParams.has('channelId') || searchParams.has('tab')) {
      setSearchParams(updateClientDistQuery(searchParams, { channelId: null, tab: null }), { replace: true })
    }
  }

  if (selected) {
    return (
      <ChannelWorkbench
        channelId={selected}
        onBack={backToList}
      />
    )
  }

  const list = channels ?? []
  const isEmpty = list.length === 0 && !isLoading

  return (
    // 全量对齐（视图 1 · 频道列表）：外壳与页头改用布局层原语。
    // data-page 保持 client-channels 原值。
    <PageShell data-page="client-channels">
      <PageHeader
        title={t('nav.clientChannels')}
        description={t('clientChannels.subtitle', '管理客户端分发频道与拉取密钥。每服一个频道，密钥用于玩家侧更新器拉取。')}
        actions={
          !isEmpty && (
            <Button onClick={() => setCreateOpen(true)} className="shrink-0">
              <Plus className="size-4" /> {t('clientChannels.addChannel', '新增频道')}
            </Button>
          )
        }
      />

      <ClientDistFlowGuide />

      {isEmpty ? (
        <EmptyChannelsGuide onCreate={() => setCreateOpen(true)} />
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {list.map((ch: ClientChannel) => (
            <ChannelCard
              key={ch.id}
              channel={ch}
              onOpen={() => {
                setSearchParams(updateClientDistQuery(searchParams, { channelId: ch.channelId }), { replace: true })
              }}
            />
          ))}
        </div>
      )}

      <CreateChannelDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(id) => {
          setSearchParams(updateClientDistQuery(searchParams, { channelId: id }), { replace: true })
        }}
        onCreate={async (body) => {
          await createChannel.mutateAsync(body)
        }}
        submitting={createChannel.isPending}
        onNotify={(level, message) => (level === 'success' ? toast.success(message) : toast.error(message))}
      />
    </PageShell>
  )
}



/**
 * 频道工作台：顶部就绪度步骤器（状态由 keyCount/currentVersion 推导）+
 * 密钥 / 版本 / 统计 / 接入指引 分段。取代原 ChannelDetail，全程模态化。
 * 频道名由顶栏面包屑末级承载（ConsoleHeader leaf）；返回仍给页内显式按钮，避免只靠面包屑。
 */

function ChannelWorkbench({
  channelId,
  onBack,
}: {
  channelId: string
  onBack: () => void
}) {
  const { t } = useTranslation()
  const { data: detail, isLoading } = useClientChannel(channelId)
  const del = useDeleteClientChannel()
  const [searchParams] = useSearchParams()
  const securitySummary = useClientChannelSecuritySummary(channelId)
  const revokeKey = useRevokeClientKey()
  const revealKey = useRevealClientKey()
  const createKey = useCreateClientKey()
  const updateKey = useUpdateClientKey()
  const { allowed: dangerAllowed } = useDangerPermission('platform')
  const securityHref = buildClientDistHref('/client-dist-ops', searchParams, { channelId, tab: 'logs' })

  const [tab, setTab] = useTabParam<WorkbenchTab>('tab', 'keys', ['keys', 'versions', 'core', 'stats', 'guide'])
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
    // 全量对齐（视图 2 · 频道工作台）：外壳与页头改用布局层原语。
    // 页头按确认后的方案：**标题（频道名）+ channelId 小字都显示**——原先标题是 sr-only，
    // 视觉上只有 channelId，用户看不到频道名。现把名字提为可见标题，id 落进 meta（等宽），
    // 复制/排查时仍一眼可见。面包屑承载层级，与实例/节点详情同形态。
    // 「返回列表」按钮保留：原型要求「页面的『返回』保留进入之前的筛选」，纯面包屑不足以
    // 承载这一步（它还要清掉当前 channelId 上下文）。
    <PageShell data-page="client-channel-workbench">
      <ObjectPageHeader
        breadcrumbs={[
          { label: t('nav.clientChannels'), to: '/client-channels' },
          { label: detail?.name ?? channelId },
        ]}
        title={detail?.name ?? channelId}
        meta={[{ label: 'channelId', value: <span className="font-mono">{channelId}</span> }]}
        actions={
          <>
            <Button variant="ghost" size="sm" className="-ml-1.5 shrink-0 text-muted-foreground" onClick={onBack}>
              <ArrowLeft className="size-4" />
              {t('clientChannels.backToList', '返回列表')}
            </Button>
            <button
              className="text-destructive hover:underline text-sm"
              onClick={() => setDeleteChannel(true)}
            >
              {t('clientChannels.deleteChannel', '删除频道')}
            </button>
          </>
        }
        onNavigate={(to: string) => {
          // 面包屑指向列表时走页内的 onBack（SPA 返回，保留进入前的状态），不整页跳转。
          if (to === '/client-channels') onBack()
        }}
      />

      <ChannelSecuritySummaryBar
        summary={securitySummary.data as ClientChannelSecuritySummary | undefined}
        isError={securitySummary.isError}
        isLoading={securitySummary.isLoading}
        securityHref={securityHref}
        renderLink={({ href, children }) => <Link to={href}>{children}</Link>}
      />

      <ReadinessStepper
        steps={steps}
        onCta={(id) => {
          setTab(STEP_META[id].goto)
          if (id === 'keys') setKeyCreateOpen(true)
        }}
      />

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList variant="line">
          <TabsTrigger value="keys">{t('clientChannels.manageKeys', '拉取密钥')}</TabsTrigger>
          <TabsTrigger value="versions">{t('clientVersions.tab', '版本管理')}</TabsTrigger>
          <TabsTrigger value="core">{t('clientCore.tab', 'Core 版本')}</TabsTrigger>
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
            onReveal={(key) => revealKey.mutateAsync({ channelId, keyId: key.id })}
            onRevoke={async (key) => {
              await revokeKey.mutateAsync({ channelId, keyId: key.id })
            }}
            onCreateKey={(body) => createKey.mutateAsync(body)}
            onUpdateKey={(body) => updateKey.mutateAsync(body)}
            keyMutating={createKey.isPending || updateKey.isPending}
            revealing={revealKey.isPending}
            dangerAllowed={dangerAllowed}
            onNotify={(level, message) => (level === 'success' ? toast.success(message) : toast.error(message))}
          />
        </TabsContent>
        <TabsContent value="versions">
          <ClientVersionsPanel channelId={channelId} />
        </TabsContent>
        <TabsContent value="core">
          <ClientUpdaterCoreSelector channelId={channelId} />
        </TabsContent>
        <TabsContent value="stats">
          <ClientStatsPanel channelId={channelId} />
        </TabsContent>
        <TabsContent value="guide">
          <ClientIntegrationGuide channelId={channelId} keys={detail?.keys ?? []} />
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
    </PageShell>
  )
}

