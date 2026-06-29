# 连通性自检端点族（FR-229）

> 状态：开发中 · 增强 FR-185（出站代理）/ FR-178（节点运行时）· 免改 proto

## 1. 需求

「先测后用」的连通性探测，统一三处「测试连通性」入口：

1. **代理设置测试**（FR-185 网络分类）：测 CP 能否经当前出站代理访问外网。
2. **JDK 下载源测试**（运行时分类）：测 JDK 下载源（foojay / 镜像）是否可达。
3. **节点存活测试**（JDK 一键下载前）：测节点 Worker 是否存活——不通即提示，避免对离线/卡顿节点发起会卡死的下载（对应原始诉求 #7/#10/#11）。

## 2. 设计

两个平台管理员端点（无新 proto，复用既有出站 Provider 与 `GetVersion` RPC）：

- `POST /diagnostics/http-test {url}` → 经 CP 出站客户端（`httpclient.Provider.Client`，含已配置代理 FR-185）GET 目标 URL，返回 `{ok,status,latencyMs,error}`。代理测试与 JDK 源测试共用此端点（仅 URL 不同：代理=GitHub、源=foojay）。
- `POST /nodes/:id/ping` → 经 gRPC 调 Worker 轻量 `GetVersion` 主动探活（不读心跳缓存），返回 `{alive,latencyMs,version,os,arch,error}`。

**边界**：出站 HTTP 测试从 CP 侧发起（经 CP 出站代理），反映「CP 能否到达该源」；Worker 侧实际下载的可达性差异（如 Worker 独立代理）不在范围（避免新增 Worker RPC）。出站测试可让 CP 请求任意 URL（SSRF 面）→ 限平台管理员。

服务 `DiagnosticsService{db,pool,outbound}`；前端 `useTestHTTP`/`usePingNode` + 复用组件 `OutboundTestButton`（代理/源）+ `PingNodeButton`（节点）。

## 3. 验收

- [x] `POST /diagnostics/http-test` 仅放行带 host 的 http/https 绝对 URL，非法 400；可达返 ok+status+latency，连接失败返 ok=false+error（非 5xx）。
- [x] `POST /nodes/:id/ping` 在线返 alive+version+latency；离线/未连接返 alive=false（非 5xx）；节点不存在 404。
- [x] 设置·网络有「测试出站连通性」、设置·运行时有「测试 JDK 下载源」、JDK 一键下载页有「测试节点存活」，行内显示结果。
- [x] 后端单测（URL 校验 / 可达 / 节点不存在 / 离线）+ 前端 tsc/lint/vitest 绿。
- [x] **真机验**：节点存活经真 Worker GetVersion 返「在线 · v0.12.0 · 0ms」；JDK 源经真 CP 出站返「可达 · 200 · 371ms」。

## 4. 关联

代码：`internal/controlplane/service/diagnostics.go`、`internal/controlplane/router/diagnostics.go`、`web/src/api/diagnostics.ts`、`web/src/components/{OutboundTestButton,PingNodeButton}.tsx`、`web/src/pages/SettingsPage.tsx`、`web/src/components/NodeJDKPanel.tsx`。
