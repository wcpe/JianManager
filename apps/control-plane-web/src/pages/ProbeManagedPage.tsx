/**
 * @file 探针托管总控页：把「配置编辑 / 下发编排 / 对账矩阵」收敛到单页 3 Tab。
 *
 * ## 为什么合并
 * 原来三件事各占一个一级导航入口（配置层、下发会话、对账矩阵），加上平台其它入口，
 * 一级路由过多、用户需要在三个页面间来回跳才能完成"改配置 → 下发 → 看结果"这一条链路。
 * 合并后侧栏只留一个「探针托管」，流程在页内闭环。
 *
 * ## Tab 的分工（按用户动作顺序，不是按技术模块）
 * 1. **配置与下发**：写配置 → 影响预演 → 保存 → 建会话 → 推进。用户在这里完成"我要改什么"。
 * 2. **对账矩阵**：看清"现在每台机器实际是什么状态"。读侧视图，研判用。
 * 3. **下发历史**：翻过去的会话，看失败明细、回滚。审计与复盘用。
 *
 * Tab 状态走 URL query（`?tab=`），刷新与分享链接都能回到同一处。
 */
import { Suspense, lazy, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { Spinner } from '@jianmanager/ui/components/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'

import { ROUTE_CHUNKS } from '@/lib/shared/route-chunks'

// 复用既有页面：它们各自已经完整实现，此处只做容器与 Tab 编排，不复制内部逻辑。
const ManagedConfigPage = lazy(ROUTE_CHUNKS['/probe/managed'])
const ManagedMatrixPage = lazy(ROUTE_CHUNKS['/probe/managed/matrix'])

/** 三个 Tab 的键。 */
const TABS = ['config', 'matrix', 'history'] as const
type ManagedTab = (typeof TABS)[number]

/** 从 query 读 tab，非法值回退到 config（分享链接带脏参数时不至于白屏）。 */
function readTab(raw: string | null): ManagedTab {
  return raw && (TABS as readonly string[]).includes(raw) ? (raw as ManagedTab) : 'config'
}

/** Tab 懒加载占位：避免切换瞬间整块空白。 */
function TabFallback() {
  return (
    <div className="flex items-center justify-center py-16" data-testid="managed-tab-loading">
      <Spinner />
    </div>
  )
}

/**
 * 探针托管总控页。
 *
 * 注意：`ManagedConfigPage` 内部自带「配置层 / 下发会话」两个 Tab，这里把它的两个 Tab
 * 拆成外层两个 Tab（config / history）——外层控制**页面级**意图，
 * 内层那个页面的 Tab 逻辑保持原样，避免改动已验证过的代码。
 */
export default function ProbeManagedPage() {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const tab = useMemo(() => readTab(params.get('tab')), [params])

  const onTabChange = (next: string) => {
    const nextParams = new URLSearchParams(params)
    nextParams.set('tab', next)
    setParams(nextParams, { replace: true })
  }

  return (
    <Tabs value={tab} onValueChange={onTabChange} className="flex flex-col gap-4">
      <TabsList>
        <TabsTrigger value="config" data-testid="managed-tab-config">
          {t('probeManaged.tabConfig', '配置与下发')}
        </TabsTrigger>
        <TabsTrigger value="matrix" data-testid="managed-tab-matrix">
          {t('probeManaged.tabMatrix', '对账矩阵')}
        </TabsTrigger>
        <TabsTrigger value="history" data-testid="managed-tab-history">
          {t('probeManaged.tabHistory', '下发历史')}
        </TabsTrigger>
      </TabsList>

      <TabsContent value="config" className="flex flex-col gap-3">
        <Suspense fallback={<TabFallback />}>
          <ManagedConfigPage defaultTab="layers" />
        </Suspense>
      </TabsContent>

      <TabsContent value="matrix" className="flex flex-col gap-3">
        <Suspense fallback={<TabFallback />}>
          <ManagedMatrixPage />
        </Suspense>
      </TabsContent>

      <TabsContent value="history" className="flex flex-col gap-3">
        <Suspense fallback={<TabFallback />}>
          <ManagedConfigPage defaultTab="pushes" />
        </Suspense>
      </TabsContent>
    </Tabs>
  )
}
