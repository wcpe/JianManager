import BcPlayersPanel from '@/components/views/console/BcPlayersPanel'
import { useOnlinePlayers } from '@/api/players'
import { useTopology } from '@/api/topology'

/**
 * 跨服玩家分布接线层（ADR-097）：注入该代理的注册列表与在线探针结果，
 * 消费方（InstanceConsolePage）零改动。
 */
export default function BcPlayersPanelConnected({ instanceId }: { instanceId: number }) {
  const { data: topology } = useTopology()
  const { data: online } = useOnlinePlayers()
  const proxy = topology?.proxies.find((p) => p.id === instanceId)
  return <BcPlayersPanel registrations={proxy?.registrations} online={online} />
}
