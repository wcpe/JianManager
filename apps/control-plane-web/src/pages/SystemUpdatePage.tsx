// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做三份查询取数、八个写动作、
// 四处危险确认的门禁注入与全部 toast 文案。
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useSelfUpdateCheck, useRefreshSelfUpdateCheck, useRollout, useWorkerAssets, useCacheWorkerAsset, useUpgradeControlPlane, useUpgradeNode, useUpgradeAll, useRollbackControlPlane, useRollbackNode } from '@/api/selfUpdate'
import { useAuthStore } from '@/stores/auth'
import { useDangerPermission } from '@/lib/shared/danger'
import { SystemUpdatePageView } from '@/components/views/system-update/SystemUpdatePageView'
import type { SystemUpdateNodePending, SystemUpdateRolloutDraft, SystemUpdateWorkerAssetTarget } from '@/components/views/system-update/SystemUpdatePageView'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * 面板自更新页容器（ADR-097 a 范式）：检查结果 / 全网升级进度 / Worker 二进制缓存三份查询、
 * 八个写动作（检查、CP 升级回滚、节点升级回滚、全网升级、预缓存）、四处危险确认的平台门禁
 * 与全部 toast 文案都在这里决定；版本对比卡片、节点区、缓存面板与进度面板交共享视图。
 *
 * 受控状态归属：本页三份查询的查询键固定，没有「一变就重新取数」的状态，故弹窗开合与金丝雀
 * 草稿（视图侧纯 UI 状态）都留视图；容器只从 mutation 派生在途展示态（`cpUpgrading` /
 * `cpRollingBack` / `nodePending` / `pendingWorkerAsset`），因为视图不持 mutation 实例。
 * 角色兜底（`ROLE_PLATFORM_ADMIN`）与危险确认门禁（`useDangerPermission('platform')`）同样是
 * 应用状态，故在此判定后以布尔注入——包内组件不得读鉴权 store。
 * 保留同路径默认导出，路由表（`ROUTE_CHUNKS['/system-update']`）与既有用例无需改动。
 */
export default function SystemUpdatePage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isPlatformAdmin = role === ROLE_PLATFORM_ADMIN

  // check 读服务端缓存（进页即时回显，FR-186）；refresh 走 live 检查并覆盖缓存。
  const check = useSelfUpdateCheck()
  const refresh = useRefreshSelfUpdateCheck()
  const upgradeAll = useUpgradeAll()
  const upgradeCp = useUpgradeControlPlane()
  const rollbackCp = useRollbackControlPlane()
  const upgradeNode = useUpgradeNode()
  const rollbackNode = useRollbackNode()
  const cacheAsset = useCacheWorkerAsset()

  // FIX-6：进页只读缓存（useSelfUpdateCheck = GET /check，无副作用）、绝不自动 live 刷新。
  // 原「进页静默刷新一次」每次点开都触发 live 检查 + UPDATE self_update_check_caches（慢 + 写库 + 联网），
  // 改为仅「检查更新」按钮显式 live 刷新；缓存为空时页面提示点击检查更新。

  // rollout 在运行中时短轮询，空闲/完成后停（轮询逻辑在 hook 内）。
  const rolloutQ = useRollout()

  // Worker 缓存状态原挂在节点区的缓存面板内，而该面板只在「检查结果到手」后才挂载——未挂载即不发请求；
  // 视图受控化后 hook 归容器，故用 `enabled` 门控保住同一取数时机（同 api/alerts 的 useAlertEvents）。
  const assets = useWorkerAssets({ enabled: !!check.data })

  // 四处危险确认统一 scope=platform：角色门禁在应用侧判定后注入（包内不持鉴权状态）。
  const { allowed: dangerAllowed } = useDangerPermission('platform')

  /** 升级/回滚成功后重新取检查结果（CP 升级会重启，故延迟 4s 等重连）。 */
  const refreshCheck = () => refresh.mutate(undefined)

  // 手动「检查更新」= 显式 live 刷新（失败 toast 但保留旧缓存数据，FR-186）。
  const doRefresh = () => {
    refresh.mutate(undefined, {
      onError: (e) => toast.error(errMsg(e, t('systemUpdate.checkFailed', '检查更新失败'))),
    })
  }

  /**
   * 全网升级（FR-155）：视图把金丝雀草稿原样上报，这里折算请求体——
   * 空输入=不设该项：canarySize 省略=无金丝雀、batchSize 省略=剩余全部一批（等价原行为）；
   * 未设金丝雀时不传 abortOnCanaryFailure（没有金丝雀可中止）。
   */
  const doUpgradeAll = (draft: SystemUpdateRolloutDraft) => {
    const canary = draft.canarySize.trim() === '' ? undefined : Math.max(0, Number(draft.canarySize))
    const batch = draft.batchSize.trim() === '' ? undefined : Math.max(0, Number(draft.batchSize))
    upgradeAll.mutate(
      {
        canarySize: canary,
        batchSize: batch,
        abortOnCanaryFailure: canary ? draft.abortOnCanaryFailure : undefined,
      },
      {
        onSuccess: () => {
          toast.success(t('systemUpdate.rolloutStarted', '全网升级已发起'))
          void rolloutQ.refetch()
        },
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.rolloutStartFailed', '发起全网升级失败'))),
      },
    )
  }

  const doUpgradeControlPlane = () => {
    upgradeCp.mutate(undefined, {
      onSuccess: (ack) => {
        toast.success(
          t('systemUpdate.cpUpgradeStarted', '控制台升级已开始（{{from}} → {{to}}），即将平滑重启', {
            from: ack.fromVersion,
            to: ack.toVersion,
          }),
        )
        // CP 升级后会重启，稍后刷新检查结果（重连后版本应为新版）。
        setTimeout(refreshCheck, 4000)
      },
      onError: (e) => toast.error(errMsg(e, t('systemUpdate.cpUpgradeFailed', '控制台升级失败'))),
    })
  }

  const doRollbackControlPlane = () => {
    rollbackCp.mutate(undefined, {
      onSuccess: (ack) => {
        toast.success(
          t('systemUpdate.cpRollbackStarted', '控制台已回滚（{{from}} → {{to}}），即将平滑重启', {
            from: ack.fromVersion,
            to: ack.toVersion,
          }),
        )
        setTimeout(refreshCheck, 4000)
      },
      onError: (e) => toast.error(errMsg(e, t('systemUpdate.cpRollbackFailed', '控制台回滚失败'))),
    })
  }

  const doUpgradeNode = (nodeId: number) => {
    upgradeNode.mutate(
      { nodeId },
      {
        onSuccess: (ack) => {
          toast.success(t('systemUpdate.nodeUpgraded', '节点已升级（{{from}} → {{to}}）', { from: ack.fromVersion, to: ack.toVersion }))
          refreshCheck()
        },
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.nodeUpgradeFailed', '节点升级失败'))),
      },
    )
  }

  const doRollbackNode = (nodeId: number) => {
    rollbackNode.mutate(
      { nodeId },
      {
        onSuccess: (ack) => {
          // 回滚 ack 的 toVersion 为空时回落到该节点的备份版本（与迁包前同口径）。
          const backupVersion = check.data?.nodes.find((n) => n.nodeId === nodeId)?.backupVersion
          toast.success(t('systemUpdate.nodeRolledBack', '节点已回滚（{{from}} → {{to}}）', { from: ack.fromVersion, to: ack.toVersion || backupVersion }))
          refreshCheck()
        },
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.nodeRollbackFailed', '节点回滚失败'))),
      },
    )
  }

  const doCacheWorkerAsset = (target: SystemUpdateWorkerAssetTarget) => {
    cacheAsset.mutate(
      { os: target.os, arch: target.arch },
      {
        onSuccess: () => toast.success(t('systemUpdate.workerAssetCached', 'Worker 二进制已预缓存')),
        onError: (e) => toast.error(errMsg(e, t('systemUpdate.workerAssetCacheFailed', 'Worker 二进制预缓存失败'))),
      },
    )
  }

  /**
   * 单节点在途目标（纯展示态，不参与任何门禁）。
   *
   * 原实现每行各持一个 mutation 实例，归容器后收敛为「单实例 + 目标标记」：取消 `isPending` 后
   * `variables` 仍在，故必须同时判 isPending。差异仅限极端并发——先发起的那一行会在第二个节点动作
   * 发起后提前恢复可点（可能多一次重复提交），与四处确认框的 scope/allowed 门禁无关。
   */
  const nodePending: SystemUpdateNodePending | null =
    upgradeNode.isPending && upgradeNode.variables
      ? { nodeId: upgradeNode.variables.nodeId, action: 'upgrade' }
      : rollbackNode.isPending && rollbackNode.variables
        ? { nodeId: rollbackNode.variables.nodeId, action: 'rollback' }
        : null
  // 预缓存的在途目标：同样从 mutation 变量派生（迁包前由面板自己读 isPending + variables）。
  const pendingWorkerAsset = cacheAsset.isPending ? (cacheAsset.variables ?? null) : null

  return (
    <SystemUpdatePageView
      isPlatformAdmin={isPlatformAdmin}
      dangerAllowed={dangerAllowed}
      check={check.data}
      // 刷新失败保留旧缓存数据，仅在「从未有过任何结果」时才整屏报错（视图侧判 check 是否为空）。
      checkErrorMessage={check.isError ? errMsg(check.error, t('systemUpdate.checkFailed', '检查更新失败')) : undefined}
      refreshing={refresh.isPending || check.isFetching}
      onRefresh={doRefresh}
      cpUpgrading={upgradeCp.isPending}
      cpRollingBack={rollbackCp.isPending}
      onUpgradeControlPlane={doUpgradeControlPlane}
      onRollbackControlPlane={doRollbackControlPlane}
      nodePending={nodePending}
      onUpgradeNode={doUpgradeNode}
      onRollbackNode={doRollbackNode}
      onUpgradeAll={doUpgradeAll}
      rollout={rolloutQ.data}
      workerAssets={assets.data}
      workerAssetsFetching={assets.isFetching}
      workerAssetsErrorMessage={assets.isError ? errMsg(assets.error, t('systemUpdate.workerAssetsLoadFailed', 'Worker 二进制缓存状态加载失败')) : undefined}
      pendingWorkerAsset={pendingWorkerAsset}
      onCacheWorkerAsset={doCacheWorkerAsset}
      // 复制 SHA256 的回执：包内不引 sonner，文案由视图算好后经此通道上报。
      onNotify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
