import { toast } from 'sonner'
import {
  useBanPlayer,
  useBans,
  useKickPlayer,
  useOnlinePlayers,
  useUnbanPlayer,
  useWhitelist,
  useWhitelistAction,
} from '@/api/players'
import InstancePlayersSegmentView from '@jianmanager/ui/components/views/instances/InstancePlayersSegment'

/**
 * 实例「玩家」分区的应用接线层（ADR-097 a 范式）。
 *
 * 分区本体已迁入组件库并受控；本层取三份数据（在线聚合 / 封禁 / 白名单）、接五个动作
 * （踢/封/解封/白名单增删，scope 一律限定本实例）、把提示交给 toast。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function InstancePlayersSegment({ instanceId }: { instanceId: number }) {
  const { data: online, isLoading: onlineLoading } = useOnlinePlayers()
  const { data: bans, isLoading: bansLoading } = useBans()
  const {
    data: whitelist,
    isLoading: whitelistLoading,
    isError: whitelistError,
    refetch: refetchWhitelist,
  } = useWhitelist(instanceId)

  const kick = useKickPlayer()
  const ban = useBanPlayer()
  const unban = useUnbanPlayer()
  const whitelistAction = useWhitelistAction(instanceId)

  return (
    <InstancePlayersSegmentView
      instanceId={instanceId}
      online={online}
      onlineLoading={onlineLoading}
      bans={bans ?? []}
      bansLoading={bansLoading}
      whitelist={whitelist}
      whitelistLoading={whitelistLoading}
      whitelistError={whitelistError}
      onRetryWhitelist={() => void refetchWhitelist()}
      onKick={(payload) =>
        kick.mutateAsync({ name: payload.name, scope: { instanceId, reason: payload.reason } })
      }
      onBan={(payload) =>
        ban.mutateAsync({ name: payload.name, scope: { instanceId, reason: payload.reason } })
      }
      onUnban={(name) => unban.mutateAsync({ name }).then(() => undefined)}
      onWhitelistAction={(payload) => whitelistAction.mutateAsync(payload).then(() => undefined)}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
