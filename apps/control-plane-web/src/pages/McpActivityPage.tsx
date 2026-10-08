// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做平台管理员门禁、按窗口取数、刷新与跳转接线。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Link } from 'react-router'
import { useAuthStore } from '@/stores/auth'
import { useMcpActivity, mcpBaseUrl } from '@/api/agentObservability'
import { copyToClipboard } from '@jianmanager/ui/lib/clipboard'
import { Button } from '@jianmanager/ui/components/button'
import {
  McpActivityPageView,
  WINDOW_PRESETS,
  type McpWindowPreset,
} from '@jianmanager/ui/components/views/agent/McpActivityPageView'

const ROLE_PLATFORM_ADMIN = 10

/**
 * 窗口档位常量定义在包内（档位按钮的实际渲染处），此处原样再导出：
 * McpActivityPage.dom.test.tsx 用 `import { WINDOW_PRESETS } from './McpActivityPage'` 逐项枚举，
 * 校验「Go duration 形态 + 1h~168h 区间」这一后端可解析性契约（完整说明见包内常量的注释）。
 * 经本文件再导出，该守卫才继续盯住包内真正渲染的档位值，而不是一份副本。
 */
export { WINDOW_PRESETS }

/**
 * MCP 活动运维页（FR-391 / ADR-096）容器：平台管理员门禁、按窗口取数（端点无会话可观测，
 * 靠轮询兜底实时性）、手动刷新、端点复制提示与页头两个跳转入口都在这里决定，
 * 表格与三态展示交共享视图（ADR-097）。保留同路径默认导出，路由表无需改动。
 */
export default function McpActivityPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isAdmin = role === ROLE_PLATFORM_ADMIN
  // 默认档位 24h（即 WINDOW_PRESETS 首档）：切档会改查询键并重新取数，属「取数时机」决策，故 state 归容器。
  const [windowValue, setWindowValue] = useState<McpWindowPreset>('24h')

  const { data, isLoading, isError, refetch, isFetching } = useMcpActivity(windowValue, {
    enabled: isAdmin,
  })

  // 非管理员不渲染页面、也不发请求（enabled 已收敛，此处只是不再挂视图）。
  if (!isAdmin) {
    return <p className="text-sm text-muted-foreground">{t('mcpActivity.forbidden')}</p>
  }

  const onCopyUrl = async () => {
    const ok = await copyToClipboard(mcpBaseUrl())
    if (ok) toast.success(t('mcpActivity.copied'))
    else toast.error(t('mcpActivity.copyFailed'))
  }

  return (
    <McpActivityPageView
      window={windowValue}
      onWindowChange={setWindowValue}
      items={data?.items ?? []}
      generatedAt={data?.generatedAt}
      isLoading={isLoading}
      isError={isError}
      isFetching={isFetching}
      onRefresh={() => void refetch()}
      endpoint={mcpBaseUrl()}
      onCopyEndpoint={() => void onCopyUrl()}
      headerLinks={
        <>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-call-logs">{t('mcpActivity.openLogs')}</Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-tokens">{t('mcpActivity.revokeToken')}</Link>
          </Button>
        </>
      }
    />
  )
}
