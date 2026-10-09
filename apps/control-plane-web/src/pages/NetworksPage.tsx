// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做列表/详情取数、五个写动作、路由（视图切换 + 详情深链）
// 以及两个插槽实现：拓扑接线层（自行取数）与实例候选的防抖服务端搜索。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import {
  useAddNetworkMembers,
  useCreateNetwork,
  useDeleteNetwork,
  useNetwork,
  useNetworkAction,
  useNetworks,
  useRemoveNetworkMember,
} from '@/api/networks'
import { useInstanceSearch } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useDebounced } from '@/lib/use-debounced'
import TopologyGraph from '@/components/console/TopologyGraph'
import {
  NetworkInstancePickerView,
  NetworksPageView,
  type NetworksInstancePickerArgs,
  type NetworkView,
} from '@/components/views/networks/NetworksPageView'

/**
 * 成员候选的默认窗口。靠键入下发服务端 q 缩小，与 GroupMembersDialog（FR-336）同款；
 * 「一次取多少条」属应用侧策略，故留在容器而非组件库。
 */
const CANDIDATE_LIMIT = 50

/**
 * 详情面板右栏（实例候选）的插槽实现。
 *
 * 独立成本文件内的局部组件：插槽实现要用 hooks，而它只在详情面板挂载时被渲染，
 * 于是「详情未打开就不拉候选/节点」这一取数时机与迁包前完全一致（`useNodes` 同样只在此时发请求）。
 * 候选走服务端搜索（千级实例不得一次拉全量）：默认前 CANDIDATE_LIMIT 条，键入经 300ms 防抖下发 `q`。
 * 「排除已入组成员」留在组件库侧（成员数远小于实例数，代价可忽略），故这里只回传 `memberIds`。
 */
function NetworkInstanceCandidates({ selected, onToggle, memberIds, onAdd, adding }: NetworksInstancePickerArgs) {
  const [kw, setKw] = useState('')
  const q = useDebounced(kw, 300).trim()
  const { data: page } = useInstanceSearch({
    ...(q ? { q } : {}),
    page: 1,
    pageSize: CANDIDATE_LIMIT,
    sort: 'name',
    order: 'asc',
  })
  const { data: nodes } = useNodes()

  return (
    <NetworkInstancePickerView
      items={page?.items}
      total={page?.total}
      selected={selected}
      onToggle={onToggle}
      memberIds={memberIds}
      nodes={nodes}
      onQueryChange={setKw}
      onAdd={onAdd}
      adding={adding}
    />
  )
}

/** 从 mutation 错误里取后端错误码/消息（仅创建群的冲突码有专门文案）。 */
function createErrorOf(err: unknown): { code?: string; message?: string } {
  const data = (err as { response?: { data?: { error?: string; message?: string } } })?.response?.data
  return { code: data?.error, message: data?.message }
}

/**
 * 群组（Network 软标签）管理页容器（ADR-097 a+b 范式）：列表/详情取数、五个写动作与 toast 文案
 * 都在这里决定，页面骨架、列表、拓扑外壳、新建弹窗与详情双栏交共享视图。
 *
 * 两处受控状态归容器：`view`（切换会换掉数据源）与 `detailId`（`useNetwork` 的查询键）——
 * 二者本就由路由/查询参数派生（FR-145 可寻址：详情可深链 `?network=`，视图可走 `/networks/topology`），
 * 故容器的 `onViewChange` / `onOpenDetail` / `onCloseDetail` 做的正是原页面的导航。
 * 两个插槽把应用侧策略留在本层：拓扑接线层 `TopologyGraph`（自行取数）与候选的服务端搜索。
 * 保留原路径与原默认导出，路由表（route-chunks 的 `/networks` 与 `/networks/topology` 精确匹配）无需改动。
 */
export default function NetworksPage() {
  const { t } = useTranslation()
  const location = useLocation()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()

  const { data: networks, isLoading } = useNetworks()

  // 详情与视图存入 URL，支持深链 / 刷新还原（FR-145 可寻址；完整可寻址归 FR-128）。
  const detailId = searchParams.get('network') ? Number(searchParams.get('network')) : null
  const view: NetworkView =
    location.pathname.startsWith('/networks/topology') || searchParams.get('view') === 'topology'
      ? 'topology'
      : 'list'

  // 0 表示「无详情」：useNetwork 以 id 真值门控，等价于迁包前「详情面板未挂载即不请求」。
  const { data: detail } = useNetwork(detailId ?? 0)

  const create = useCreateNetwork()
  const del = useDeleteNetwork()
  const addMembers = useAddNetworkMembers(detailId ?? 0)
  const removeMember = useRemoveNetworkMember(detailId ?? 0)
  const action = useNetworkAction(detailId ?? 0)

  const setView = (v: NetworkView) => {
    const next = new URLSearchParams(searchParams)
    next.delete('view')
    const search = next.toString()
    navigate({ pathname: v === 'topology' ? '/networks/topology' : '/networks', search: search ? `?${search}` : '' })
  }
  const openDetail = (id: number) => {
    const next = new URLSearchParams(searchParams)
    next.set('network', String(id))
    setSearchParams(next)
  }
  const closeDetail = () => {
    const next = new URLSearchParams(searchParams)
    next.delete('network')
    setSearchParams(next)
  }

  return (
    <NetworksPageView
      view={view}
      onViewChange={setView}
      networks={networks}
      isLoading={isLoading}
      detailId={detailId}
      detail={detail}
      onOpenDetail={openDetail}
      onCloseDetail={closeDetail}
      creating={create.isPending}
      onCreate={async (input) => {
        try {
          await create.mutateAsync(input)
          toast.success(t('networks.created'))
          // 成功：视图据此关窗。
          return true
        } catch (err) {
          const { code, message } = createErrorOf(err)
          if (code === 'NETWORK_NAME_CONFLICT') toast.error(t('networks.nameConflict'))
          else toast.error(message || t('networks.createFailed'))
          // 失败：不关窗、不清草稿，便于修正后重试。
          return false
        }
      }}
      onDelete={(n) => {
        del.mutate(n.id, {
          onSuccess: () => toast.success(t('networks.deleted')),
          onError: () => toast.error(t('common.error')),
        })
      }}
      addingMembers={addMembers.isPending}
      removingMember={removeMember.isPending}
      batchPending={action.isPending}
      onAddMembers={async (instanceIds) => {
        try {
          await addMembers.mutateAsync(instanceIds)
          toast.success(t('networks.added', { count: instanceIds.length }))
          return true
        } catch {
          toast.error(t('common.error'))
          return false
        }
      }}
      onRemoveMember={async (instanceId) => {
        try {
          await removeMember.mutateAsync(instanceId)
          toast.success(t('networks.memberRemoved'))
        } catch {
          toast.error(t('common.error'))
        }
      }}
      onBatchAction={(act) => {
        action.mutate(act, {
          onSuccess: (res) => toast.success(t('networks.batchResult', { succeeded: res.succeeded, failed: res.failed })),
          onError: () => toast.error(t('common.error')),
        })
      }}
      renderTopology={() => <TopologyGraph />}
      renderInstancePicker={(args) => <NetworkInstanceCandidates {...args} />}
    />
  )
}
