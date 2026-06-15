import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

export interface ScheduleInfo {
  id: number
  uuid: string
  instanceId: number
  name: string
  cronExpr: string
  action: string
  enabled: boolean
  lastRun: string | null
  createdAt: string
}

export function useSchedules(instanceId?: number) {
  return useQuery({
    queryKey: ['schedules', instanceId],
    queryFn: async () => {
      const { data } = await api.get<ScheduleInfo[]>('/schedules', {
        params: instanceId ? { instanceId } : undefined,
      })
      return data
    },
  })
}
