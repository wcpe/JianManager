import { create } from 'zustand'

/** 工作区分段：实例的统一视图分段——终端 / 文件 / 配置 / 插件 / Bot（FR-039 合并原实例详情页，FR-052 插件）。 */
export type WorkspaceSegment = 'terminal' | 'files' | 'config' | 'plugins' | 'bot'

/**
 * 运维控制台的客户端 UI 状态（ADR-009 / FR-037 / FR-039）。
 * 仅存「当前选中节点」「当前在工作区打开的实例」与「每实例工作区分段」，不进 URL，
 * 避免与既有 `/instances/:id` 详情路由语义冲突。
 */
interface ConsoleState {
  /** 实例树节点筛选：null = 全部节点，否则为某节点 id */
  selectedNodeId: number | null
  /** 工作区当前打开的实例 id；null = 未打开任何实例 */
  openInstanceId: number | null
  /** 每个已打开实例的工作区分段（终端/Bot），按实例 id 记忆，缺省为终端 */
  workspaceSegmentByInstance: Record<number, WorkspaceSegment>
  setSelectedNodeId: (nodeId: number | null) => void
  openInstance: (instanceId: number) => void
  closeInstance: () => void
  /** 设置某实例的工作区分段（终端/Bot），持久化于本会话内 */
  setWorkspaceSegment: (instanceId: number, segment: WorkspaceSegment) => void
}

export const useConsoleStore = create<ConsoleState>((set) => ({
  selectedNodeId: null,
  openInstanceId: null,
  workspaceSegmentByInstance: {},
  setSelectedNodeId: (nodeId) => set({ selectedNodeId: nodeId }),
  openInstance: (instanceId) => set({ openInstanceId: instanceId }),
  closeInstance: () => set({ openInstanceId: null }),
  setWorkspaceSegment: (instanceId, segment) =>
    set((s) => ({
      workspaceSegmentByInstance: { ...s.workspaceSegmentByInstance, [instanceId]: segment },
    })),
}))
