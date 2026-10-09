// 视图已迁至 @jianmanager/ui（ADR-097）；本层只注入出站测试 mutation（受控化）。
import { OutboundTestButton as OutboundTestButtonView } from '@/components/views/nodes/OutboundTestButton'
import { useTestHTTP } from '@/api/diagnostics'

/**
 * 出站连通性测试按钮（FR-229，可自定义目标 FR-280）：经 CP 出站客户端（含已配置代理）GET 目标 URL，
 * 行内展示可达 + 状态码 + 往返耗时，或失败原因。供代理设置 / JDK 下载源测试复用。
 */
export function OutboundTestButton({
  defaultUrl,
  label,
  editable = false,
}: {
  defaultUrl: string
  label: string
  editable?: boolean
}) {
  const test = useTestHTTP()
  return (
    <OutboundTestButtonView
      defaultUrl={defaultUrl}
      label={label}
      editable={editable}
      onTest={(url) => test.mutate(url)}
      result={test.data}
      pending={test.isPending}
      isError={test.isError}
    />
  )
}
