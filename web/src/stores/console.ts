import { create } from 'zustand'

/**
 * 运维控制台的客户端 UI 状态（ADR-009 / FR-037）。
 * 仅存「当前选中节点」与「当前在工作区打开终端的实例」，不进 URL，
 * 避免与既有 `/instances/:id` 详情路由语义冲突。
 */
interface ConsoleState {
  /** 实例树节点筛选：null = 全部节点，否则为某节点 id */
  selectedNodeId: number | null
  /** 工作区当前打开终端的实例 id；null = 未打开任何终端 */
  openInstanceId: number | null
  setSelectedNodeId: (nodeId: number | null) => void
  openInstance: (instanceId: number) => void
  closeInstance: () => void
}

export const useConsoleStore = create<ConsoleState>((set) => ({
  selectedNodeId: null,
  openInstanceId: null,
  setSelectedNodeId: (nodeId) => set({ selectedNodeId: nodeId }),
  openInstance: (instanceId) => set({ openInstanceId: instanceId }),
  closeInstance: () => set({ openInstanceId: null }),
}))
