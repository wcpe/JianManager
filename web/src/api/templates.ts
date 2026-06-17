import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface TemplateInfo {
  id: number
  uuid: string
  name: string
  type: string
  description: string
  startCommand: string
  defaultWorkDir: string
  downloadUrl: string
  createdAt: string
}

export function useTemplates() {
  return useQuery({
    queryKey: ['templates'],
    queryFn: async () => {
      const { data } = await api.get<TemplateInfo[]>('/templates')
      return data
    },
  })
}

export function useCreateTemplate() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: {
      name: string
      type: string
      description?: string
      startCommand: string
      downloadUrl?: string
    }) => api.post('/templates', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['templates'] }),
  })
}
