import { Link } from 'react-router'

import BcSegment from '@jianmanager/ui/components/views/console/BcSegment'
import { useTopology } from '@/api/topology'

/**
 * BC 子服拓扑分段接线层（ADR-097）：从 `/topology` 取数并按 instanceId 过滤后注入，
 * 并注入子服链接的渲染（包内不依赖路由库）。消费方（InstanceConsolePage）零改动。
 */
export default function BcSegmentConnected({ instanceId }: { instanceId: number }) {
  const { data: topology } = useTopology()
  const proxy = topology?.proxies.find((p) => p.id === instanceId)
  return (
    <BcSegment
      registrations={proxy?.registrations}
      renderBackendLink={({ backendId, name }) => (
        <Link to={`/instances/${backendId}`} className="font-medium hover:text-primary hover:underline">
          {name}
        </Link>
      )}
    />
  )
}
