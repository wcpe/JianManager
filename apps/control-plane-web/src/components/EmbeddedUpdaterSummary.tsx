// 视图已迁至 @jianmanager/ui（ADR-097）；本层只注入更新器信息（受控化）。
import { EmbeddedUpdaterSummary as EmbeddedUpdaterSummaryView } from '@jianmanager/ui/components/views/client-dist/EmbeddedUpdaterParts'
import { useUpdaterJarsInfo } from '@/api/clientChannels'

/** 管理面旁路展示 Control Plane 当前内嵌更新器版本（取数接线层）。 */
export default function EmbeddedUpdaterSummary({ className = '' }: { className?: string }) {
  const { data } = useUpdaterJarsInfo()
  return <EmbeddedUpdaterSummaryView info={data} className={className} />
}
