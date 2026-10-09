// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只注入更新器信息（受控化）。
import { EmbeddedUpdaterSummary as EmbeddedUpdaterSummaryView } from '@/components/views/client-dist/EmbeddedUpdaterParts'
import { useUpdaterJarsInfo } from '@/api/clientChannels'

/** 管理面旁路展示 Control Plane 当前内嵌更新器版本（取数接线层）。 */
export default function EmbeddedUpdaterSummary({ className = '' }: { className?: string }) {
  const { data } = useUpdaterJarsInfo()
  return <EmbeddedUpdaterSummaryView info={data} className={className} />
}
