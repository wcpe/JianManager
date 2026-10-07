import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'

/** 守卫冲突中列出的实例条目。 */
export interface BlockedInstanceItem {
  id: number
  name: string
  status: string
}

export interface BlockedByInstancesDialogProps {
  /** 非空即打开。 */
  conflict: { instances: BlockedInstanceItem[] } | null
  /** 标题（由调用方按场景传入，避免组件与具体 i18n 键耦合）。 */
  title: string
  /** 描述（同样由调用方插值后传入）。 */
  description: string
  /**
   * 强制入口：label 为按钮文案、hint 为提示语；null 表示本场景不提供强制入口
   * （节点下线仅在离线时可强制，归档清理则始终可强制）。
   */
  force?: { label: string; hint: string } | null
  onClose: () => void
  onForce: () => void
}

/**
 * 节点下线 / 归档清理被实例守卫拒绝时的清单模态（FR-309 / FR-394）。
 *
 * 两个场景的差异只有文案与「是否提供强制入口」，故合并为一个受控视图：
 * 实例列表渲染、状态徽标与二次确认结构完全一致。
 */
export function BlockedByInstancesDialog({
  conflict,
  title,
  description,
  force,
  onClose,
  onForce,
}: BlockedByInstancesDialogProps) {
  const { t } = useTranslation()
  // 实例状态 → 既有 instances.* i18n 文案；未知状态原样展示兜底。
  const statusText = (status: string) => {
    const keys: Record<string, string> = {
      STOPPED: 'instances.stopped',
      STARTING: 'instances.starting',
      RUNNING: 'instances.running',
      STOPPING: 'instances.stopping',
      CRASHED: 'instances.crashed',
    }
    return keys[status] ? t(keys[status]) : status
  }
  return (
    <Dialog open={conflict !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-1.5">
          {(conflict?.instances ?? []).map((inst) => (
            <div key={inst.id} className="flex items-center justify-between gap-2 rounded-md border px-3 py-1.5 text-sm">
              <span className="min-w-0 truncate" title={inst.name}>{inst.name}</span>
              <Badge variant="outline" className="shrink-0 text-[11px] text-muted-foreground">
                {statusText(inst.status)}
              </Badge>
            </div>
          ))}
          {force && <p className="pt-1 text-xs text-muted-foreground">{force.hint}</p>}
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common.cancel')}</Button>
          {force && (
            <Button variant="destructive" onClick={onForce}>{force.label}</Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
