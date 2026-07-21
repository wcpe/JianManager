import { Badge } from '@jianmanager/ui/components/badge'

/** 标识客户端自报、不可作为授权依据的字段。 */
export default function UntrustedFieldBadge() {
  return (
    <Badge variant="outline" className="border-status-warning/40 px-1.5 py-0 text-[10px] text-status-warning">
      不可信
    </Badge>
  )
}
