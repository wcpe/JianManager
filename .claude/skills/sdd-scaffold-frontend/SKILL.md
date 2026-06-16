---
name: sdd-scaffold-frontend
description: 从前端需求生成 React 页面骨架 + API client + hooks
---

# SDD 前端脚手架

## 触发

用户说 `/sdd-scaffold-frontend <feature-name>` 或「搭一下 XXX 前端页面」

## 执行步骤

1. 读取 `docs/specs/<feature-name>/api.md` 获取 API 定义
2. 读取 `docs/ARCHITECTURE.md` 前端架构部分
3. 生成以下文件：

### API Client

**`web/src/api/<module>.ts`**
- 每个 endpoint 对应一个函数
- 使用 TanStack Query 的 `useQuery` / `useMutation`
- TypeScript 类型定义（从 API spec 的 JSON 结构推导）

```typescript
// 示例
export interface Instance {
  id: number
  uuid: string
  name: string
  status: 'STOPPED' | 'STARTING' | 'RUNNING' | 'STOPPING' | 'CRASHED'
  // ...
}

export function useInstances(params?: ListParams) {
  return useQuery({
    queryKey: ['instances', params],
    queryFn: () => api.get<Instance[]>('/instances', { params }).then(r => r.data),
  })
}

export function useStartInstance() {
  return useMutation({
    mutationFn: (id: string) => api.post(`/instances/${id}/start`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['instances'] }),
  })
}
```

### Page Components

**`web/src/pages/<module>/<PageName>.tsx`**
- 列表页：Table + 操作按钮 + 搜索/筛选
- 详情页：Tabs + 信息卡片
- 使用 shadcn/ui 组件
- 使用 `React.lazy` 导出

### 路由注册

在 `web/src/router.tsx` 中追加路由
在 `web/src/route-permissions.ts` 中追加权限映射

4. 输出已生成文件清单

## 约束

- 使用 shadcn/ui + TailwindCSS，不用自定义 CSS
- 所有页面 `React.lazy` 懒加载
- 服务端数据用 TanStack Query，不用 useEffect + useState
- TypeScript 严格类型，不用 `any`
