# 对外 API 契约

本目录是 JianManager **对外 API** 的契约真源（OpenAPI 3.1）。

## 边界

| 文档 | 面向 | 稳定性 |
|---|---|---|
| `docs/API.md` | JM 前端开发者（对内 API，用户 JWT） | 随版本可变 |
| `docs/openapi/`（本目录） | 业务台、第三方集成（对外 API，Agent Token） | **向后兼容** |
| `docs/specs/*/spec.md` | 开发者（设计意图、权衡、待定项） | 随设计演进 |

三者不重复：spec 讲**为什么**，openapi 讲**是什么契约**，API.md 讲**内部怎么用**。

## 文件结构

```
openapi/
├── external.yaml           # 入口：info / servers / tags / security / $ref 汇总
├── components/
│   ├── common.yaml         # 通用参数、响应、分页头
│   ├── errors.yaml         # 15 个错误码（每个一个 schema）
│   └── schemas.yaml        # 共享数据结构
└── paths/
    ├── business.yaml       # 业务通道（5 个接口）
    ├── stream.yaml         # SSE 事件流（1 个）
    ├── stats.yaml          # 统计数据（6 个）
    └── bridge.yaml         # 桥状态（3 个）
```

**拆分原则**：按业务域分，不按 HTTP 方法分；单文件 ≤ 200 行；只有一个入口供工具链读取。

## 设计约定

### 1. 不透明载荷

所有涉及业务的字段只约束**信封**，不约束内容：

```yaml
payload:
  description: 不透明载荷，平台不解析
  type: [object, array, string, number, boolean, 'null']
```

不写 `type: object` —— 业务可能传数组或标量，平台不该拒绝。

### 2. 动态路由

`domain.action` 是动态的，OpenAPI 无法表达枚举。用 `pattern` 约束格式，用 `description` 说明取值不枚举：

```yaml
domain:
  type: string
  pattern: '^[a-z0-9-]{1,32}$'
  description: 格式受约束，但取值不枚举——由插件注册决定
```

### 3. SSE 的表达

OpenAPI 无法直接描述事件流。做法：

- `content: text/event-stream`，schema 为 `type: string`
- 在 `description` 里用代码块写明各帧格式
- 帧的 `data` 结构单独定义为 `BusinessEventFrame` / `PingFrame` / `ReplayGapFrame`

### 4. 错误码

每个错误码一个 schema，通过 `discriminator` 精确定位，而不是笼统的 `400`。

## 校验

### 本地

```bash
# 语法校验
python3 -c "import yaml,sys;[yaml.safe_load(open(f)) for f in sys.argv[1:]]" $(find docs/openapi -name '*.yaml')

# 结构校验（需 Node）
npx @redocly/cli lint docs/openapi/external.yaml
```

### CI

`scripts/check-openapi.sh` 做了两件事：

1. **YAML 语法与 $ref 可解析**
2. **路由一致性**：OpenAPI 声明的每个 path+method 在 Gin 里真实存在；反之 `/api/v1/business/*` 与 `/api/v1/stats/*` 下的每条路由都在 OpenAPI 里声明

防止「实现了但没写文档」和「文档写了但没实现」。

## 渲染

```bash
npx @scalar/cli build docs/openapi/external.yaml -o docs/openapi/external.html
```

产出单文件 HTML，可直接托管。**不跑 Swagger UI 服务**。

## 演进规则

| 变更 | 做法 |
|---|---|
| 新增可选字段 | 直接加 |
| 新增 endpoint | 直接加 |
| 新增错误码 | 直接加（调用方应能忽略未知码） |
| 删除字段 | 先标 `deprecated: true` + `x-sunset-date`，保留一个版本周期 |
| 改字段类型 | 视为新 endpoint，走 `/v2/` |
| 改语义 | 必须改 `description` 并在 CHANGELOG 标注 |

## 明确不做

- **不生成任何代码**（不生成 Go stub，不生成 TS 类型）
- **不用注解生成**（`swaggo` 无法表达动态路由，且会污染业务代码）
- **不覆盖对内 API**（200+ 接口手写 yaml 是负担，Markdown 足够）
