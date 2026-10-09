// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做聚合取数、五类写动作与 toast 接线，以及两处插槽实现：
// 实例候选的服务端搜索（千级实例）与制品存储对账区块（其自身容器化在上一批完成）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useRuntimeAssetsOverview, useRefreshRuntimeAssets, useDeleteRuntimeJDK, useDeleteAsset, useImportAsset } from '@/api/runtimeAssets'
import { useSearchInstances } from '@/api/instances'
import { useBatchDeployPlugins } from '@/api/plugins'
import { RuntimeAssetsInstancePickerView, RuntimeAssetsPageView } from '@/components/views/runtime-assets/RuntimeAssetsPageView'
import type { RuntimeAssetsInstancePickerArgs } from '@/components/views/runtime-assets/RuntimeAssetsPageView'
import ArtifactReconcileSection from './ArtifactReconcileSection'

/** API 错误形状（占用方提示从 message + instances 字段取）。 */
type ApiError = Error & {
  response?: {
    status?: number
    data?: { message?: string; instances?: Array<{ name: string }>; reason?: string; count?: number }
  }
}

/**
 * 实例候选窗口条数：千级实例必须走服务端搜索，默认只取前 N 条 + 键入下发 `q`
 * （「一次取多少条」属应用侧策略，故留在容器而非组件库）。
 */
const CANDIDATE_LIMIT = 200

/**
 * 批量部署弹窗右栏的实例候选插槽实现。
 *
 * 独立成本文件内的局部组件：它只在弹窗挂载时被渲染，于是「弹窗不打开就不拉候选」这一取数时机
 * 与迁包前（`useSearchInstances(..., open)`）等价。候选一律走服务端搜索，不得一次拉全量；
 * 搜索词是「会触发取数」的状态，故留在本层而非视图——与迁包前逐击键下发 `q` 的时序保持一致
 * （勾选集合仍在弹窗内受控）。
 */
function RuntimeInstanceCandidates({ selected, onToggle }: RuntimeAssetsInstancePickerArgs) {
  const [keyword, setKeyword] = useState('')
  const { data: page, isLoading } = useSearchInstances({
    q: keyword || undefined,
    page: 1,
    pageSize: CANDIDATE_LIMIT,
    sort: 'name',
    order: 'asc',
  })

  return (
    <RuntimeAssetsInstancePickerView
      items={page?.items}
      loading={isLoading}
      selected={selected}
      onToggle={onToggle}
      onQueryChange={setKeyword}
    />
  )
}

/**
 * 运行时与制品全局页容器（ADR-097 b 范式）：聚合取数，以及刷新运行时库存 / 删 JDK / 删制品 /
 * 导入制品 / 批量部署插件五类写动作与 toast 文案都在这里决定；页壳、两区内容、两个对话框与
 * 两处删除二次确认交共享视图。
 *
 * 受控状态归属：会触发取数的实例搜索词在插槽实现内（本层文件）；视图只留筛选草稿、对话框开合、
 * 勾选集合等纯 UI 状态。两处删除的「在途」由各自 mutation 的变量折算成具体目标 id，
 * 保持迁包前逐卡片/逐行禁用的粒度。保留同路径默认导出，路由表与既有 DOM 测试无需改动。
 */
export default function RuntimeAssetsPage() {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useRuntimeAssetsOverview()
  const refresh = useRefreshRuntimeAssets()
  const delJdk = useDeleteRuntimeJDK()
  const delAsset = useDeleteAsset()
  const importAsset = useImportAsset()
  const deploy = useBatchDeployPlugins()

  return (
    <RuntimeAssetsPageView
      overview={data}
      isLoading={isLoading}
      isError={isError}
      refreshing={refresh.isPending}
      onRefreshRuntimes={() => {
        // 失败容忍：部分节点失败时提示失败节点名单，页面继续显示 DB 旧数据（不清空、不报错态）。
        refresh.mutate(undefined, {
          onSuccess: (outcome) => {
            const failed = outcome.results.filter((r) => !r.ok)
            if (failed.length > 0) {
              toast.warning(
                t('runtimeAssets.refreshPartial', {
                  names: failed.map((f) => f.nodeName || `#${f.nodeId}`).join('、'),
                }),
              )
            } else {
              toast.success(t('runtimeAssets.refreshDone'))
            }
          },
          onError: (err: ApiError) => toast.error(err.response?.data?.message || t('runtimeAssets.refreshFailed')),
        })
      }}
      onDeleteJdk={({ nodeId, jdkId }) => {
        delJdk.mutate({ nodeId, jdkId }, {
          onSuccess: () => toast.success(t('runtimeAssets.jdkDeleted')),
          onError: (err: ApiError) => {
            // 409 + 占用方名单：用后端给的实例名呈现「谁在用」，否则回落到后端 message。
            const occupants = err.response?.data?.instances?.map((i) => i.name).join('、')
            if (err.response?.status === 409 && occupants) {
              toast.error(t('runtimeAssets.jdkInUse', { names: occupants }))
            } else {
              toast.error(err.response?.data?.message || t('runtimeAssets.deleteFailed'))
            }
          },
        })
      }}
      // 只有容器知道在删哪一条：未在删除时为 null，所有卡片/行都可点。
      deletingJdkId={delJdk.isPending ? (delJdk.variables?.jdkId ?? null) : null}
      onDeleteAsset={({ assetId, refCount }) => {
        delAsset.mutate(assetId, {
          onSuccess: () => toast.success(t('runtimeAssets.assetDeleted')),
          onError: (err: ApiError) => {
            if (err.response?.status === 409) {
              // 引用占用：优先用后端给出的分类原因键，缺省回落到通用文案；count 缺省按行上引用数兜底。
              const reason = err.response.data?.reason
              const count = err.response.data?.count ?? refCount
              const key = reason ? `runtimeAssets.assetInUseReasons.${reason}` : 'runtimeAssets.assetInUse'
              toast.error(t(key, { count, defaultValue: t('runtimeAssets.assetInUse', { count }) }))
            } else {
              toast.error(err.response?.data?.message || t('runtimeAssets.deleteFailed'))
            }
          },
        })
      }}
      deletingAssetId={delAsset.isPending ? (delAsset.variables ?? null) : null}
      importing={importAsset.isPending}
      onImportAsset={(args) =>
        new Promise<boolean>((resolve) => {
          importAsset.mutate(
            {
              type: args.type,
              file: args.file,
              name: args.name,
              version: args.version,
              onProgress: args.onProgress,
            },
            {
              onSuccess: () => {
                toast.success(t('runtimeAssets.importDone'))
                resolve(true)
              },
              onError: (err: ApiError) => {
                toast.error(err.response?.data?.message || t('runtimeAssets.importFailed'))
                resolve(false)
              },
              // 成败都要收起视图里的进度条，否则失败时它会一直挂着。
              onSettled: () => args.onSettled(),
            },
          )
        })
      }
      deploying={deploy.isPending}
      deployResult={deploy.data ?? null}
      onBatchDeploy={(args) => deploy.mutate(args)}
      renderInstancePicker={(args) => <RuntimeInstanceCandidates {...args} />}
      // 对账区块自带取数与处置动作，故整块经插槽注入（渠道清单来自本次聚合载荷）。
      renderReconcileSection={({ channels }) => <ArtifactReconcileSection channels={channels} />}
    />
  )
}
