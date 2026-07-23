/**
 * 文件浏览场景 Capability（FR-378）。
 * 描述各入口能做什么，由 UnifiedExplorerShell / 调用方消费；不删业务能力、不硬套。
 */

import type { FileBrowserAction } from './types'

/** 壳渲染模式。 */
export type ExplorerShellMode =
  /** 共享只读/轻操作浏览器（FileBrowser + Source）。 */
  | 'browser'
  /** 实例全功能多标签资源管理器（ExplorerTabHost）。 */
  | 'instance-files'
  /** 完全自定义（children / 业务树如分发编排）。 */
  | 'custom'

export interface ExplorerCapability {
  /** 稳定 id（如 instance-browse / storage-browse）。 */
  id: string
  mode: ExplorerShellMode
  /** 是否允许写（改名/删/保存等）；browser 模式通常 false。 */
  canWrite: boolean
  /** 是否允许上传。 */
  canUpload: boolean
  /** 是否允许 chmod（FR-373）；仅实例写路径有意义。 */
  canChmod: boolean
  /** 是否展示下载。 */
  canDownload: boolean
  /** 可选业务扩展行操作（不替代分发标记等专用 UI）。 */
  extraActions?: FileBrowserAction[]
}

/** 实例「文件」全功能（多标签 + 写）。 */
export function instanceFilesCapability(): ExplorerCapability {
  return {
    id: 'instance-files',
    mode: 'instance-files',
    canWrite: true,
    canUpload: true,
    canChmod: true,
    canDownload: true,
  }
}

/** 实例「浏览」只读 + 下载。 */
export function instanceBrowseCapability(downloadAction?: FileBrowserAction): ExplorerCapability {
  return {
    id: 'instance-browse',
    mode: 'browser',
    canWrite: false,
    canUpload: false,
    canChmod: false,
    canDownload: true,
    extraActions: downloadAction ? [downloadAction] : undefined,
  }
}

/** 平台存储只读浏览（无写、无 chmod；清理 cache 在页级另做）。 */
export function storageBrowseCapability(): ExplorerCapability {
  return {
    id: 'storage-browse',
    mode: 'browser',
    canWrite: false,
    canUpload: false,
    canChmod: false,
    canDownload: false,
  }
}

/** 客户端分发只读预览（FR-214）。 */
export function clientDistBrowseCapability(actions?: FileBrowserAction[]): ExplorerCapability {
  return {
    id: 'client-dist-browse',
    mode: 'browser',
    canWrite: false,
    canUpload: false,
    canChmod: false,
    canDownload: true,
    extraActions: actions,
  }
}

/** 业务自定义壳（编排树等）。 */
export function customExplorerCapability(id: string): ExplorerCapability {
  return {
    id,
    mode: 'custom',
    canWrite: true,
    canUpload: true,
    canChmod: false,
    canDownload: false,
  }
}

/** 由 Capability 推导 FileBrowser 的 readOnly 与 actions。 */
export function browserPropsFromCapability(cap: ExplorerCapability): {
  readOnly: boolean
  actions: FileBrowserAction[]
} {
  const actions = cap.extraActions ?? []
  // 有注入操作则 readOnly=false 以渲染菜单；写能力仍由调用方端点约束
  return {
    readOnly: actions.length === 0,
    actions,
  }
}
