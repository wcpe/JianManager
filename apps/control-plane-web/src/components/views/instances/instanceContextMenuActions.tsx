import { Pencil, Play, RotateCw, Square, Tag, Trash2, Wrench } from 'lucide-react'

import type { InstanceInfo } from '@/api/instances'

/**
 * 实例「⋯」/右键菜单的动作表构造（纯函数）。
 *
 * 【为什么单独一个文件】`InstanceContextMenu.tsx` 导出的是组件，再导出一个普通函数会让
 * `react-refresh/only-export-components` 报「非组件导出」——该文件的 HMR 退化成整页刷新。
 * 把纯函数抽出来，组件文件就「只导出组件」，本文件「只导出函数」，各守其分。
 *
 * 抽出来还有个附带好处：本文件不 import React 组件库、不含 hooks，
 * 单测 `InstanceContextMenu.test.ts` 只装载这一份纯逻辑。
 */

/** 一条菜单动作。 */
export interface ContextMenuAction {
  key: string
  label: string
  icon?: React.ReactNode
  onSelect: () => void
  /** 危险动作：标红并与上方动作以分隔线隔开。 */
  danger?: boolean
  /** 禁用（如运行中不可克隆）：仍显示，但不可点。 */
  disabled?: boolean
  /** 禁用原因（悬浮提示），与菜单里 title 一致。 */
  disabledHint?: string
}

/**
 * 把页面侧的实例状态翻译成菜单项（纯函数，便于单测直接断言顺序与禁用条件）。
 *
 * 顺序按「最常用在上、破坏性在下」排：启停 → 编辑配置 → 标签 → 代理 → 克隆 → 删除。
 * 与「⋯」下拉菜单的可用性判据同源（运行态不可克隆、删除始终可用+提示）。
 */
export function buildContextMenuActions({
  inst,
  t,
  onStart,
  onStop,
  onRestart,
  onEditConfig,
  onTags,
  onLimits,
  onProxy,
  onClone,
  onDelete,
}: {
  inst: InstanceInfo
  t: (key: string, fallback: string, opts?: Record<string, unknown>) => string
  onStart: () => void
  onStop: () => void
  onRestart: () => void
  onEditConfig: () => void
  onTags: () => void
  onLimits: () => void
  onProxy: () => void
  onClone: () => void
  onDelete: () => void
}): ContextMenuAction[] {
  // 克隆要求已停止：运行/过渡态禁用并说明原因，而不是把入口藏起来（藏了用户会以为是 bug）。
  const stopped = inst.status === 'STOPPED' || inst.status === 'CRASHED'
  const running = inst.status === 'RUNNING'
  const actions: ContextMenuAction[] = []

  if (stopped) {
    actions.push({
      key: 'start',
      label: t('instances.start', '启动'),
      icon: <Play />,
      onSelect: onStart,
    })
  } else {
    actions.push({
      key: 'stop',
      label: t('instances.stop', '停止'),
      icon: <Square />,
      onSelect: onStop,
    })
  }
  if (running) {
    actions.push({
      key: 'restart',
      label: t('instances.restart', '重启'),
      icon: <RotateCw />,
      onSelect: onRestart,
    })
  }

  actions.push({
    key: 'editConfig',
    label: t('instances.editConfig', '编辑配置'),
    icon: <Pencil />,
    onSelect: onEditConfig,
  })
  actions.push({
    key: 'tags',
    label: t('grouping.editTags', '编辑标签'),
    icon: <Tag />,
    onSelect: onTags,
  })
  if (inst.processType === 'docker') {
    actions.push({
      key: 'limits',
      label: t('instances.resourceLimit', '资源限额'),
      icon: <Wrench />,
      onSelect: onLimits,
    })
  }
  if (inst.role === 'proxy') {
    actions.push({
      key: 'proxy',
      label: t('proxy.manageBackends', '管理后端'),
      icon: <Wrench />,
      onSelect: onProxy,
    })
  }
  actions.push({
    key: 'clone',
    label: t('clone.action', '克隆'),
    icon: <Wrench />,
    onSelect: onClone,
    disabled: !stopped,
    disabledHint: stopped ? undefined : t('instances.cloneRunningHint', '运行中的实例不能克隆，请先停止'),
  })

  actions.push({
    key: 'delete',
    label: t('common.delete', '删除'),
    icon: <Trash2 />,
    onSelect: onDelete,
    danger: true,
  })

  return actions
}
