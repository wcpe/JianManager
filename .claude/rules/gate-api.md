# Gate 3: API Spec → 编码门禁

> Feature 的 API Spec 必须满足以下条件才能开始编码。

## Checklist

- [ ] 所有 endpoint 已定义（路径、HTTP 方法、请求体、响应体）
- [ ] 所有 error code 已定义（业务错误码 + HTTP 状态码）
- [ ] 权限要求已标注（需要什么 permission node）
- [ ] 和 ARCHITECTURE.md 中的通信协议一致
- [ ] 和数据库模型一致（字段名、类型匹配）
- [ ] 请求/响应体的 JSON 结构可直接用于生成 TypeScript 类型
- [ ] 每个 endpoint 标注了关联的 FR 编号

## 未通过处理

补充 Feature 的 `docs/specs/<feature>/api.md`，不得写代码。

## 检查方式

按此 checklist 逐项核对。
