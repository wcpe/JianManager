# 配置文件规范

## 格式

所有配置文件使用 YAML 格式，不使用 JSON 或 TOML。

## 命名

- Control Plane：`control-plane.yaml`
- Worker Node：`worker.yaml`
- 环境变量覆盖：`JIANMANAGER_` 前缀 + `_` 分隔的路径大写
  - `server.port` → `JIANMANAGER_SERVER_PORT`
  - `database.driver` → `JIANMANAGER_DB_DRIVER`

## 配置结构

```yaml
# control-plane.yaml
server:
  host: 0.0.0.0
  port: 8080
  dev_mode: false

grpc:
  port: 9100

database:
  driver: sqlite
  dsn: data/jianmanager.db

jwt:
  secret: ${JIANMANAGER_JWT_SECRET}  # 引用环境变量
  access_ttl: 15m
  refresh_ttl: 168h

log:
  level: info     # debug | info | warn | error
  format: json    # json | text

# 首次启动引导通过 Web UI 完成（FR-017），不再使用 bootstrap 配置段
```

## 规则

1. **敏感信息不得硬编码**：密码、密钥、Token 必须通过 `${ENV_VAR}` 引用环境变量
2. **默认值必须合理**：所有配置项必须有默认值，零配置即可启动开发环境
3. **配置项必须在代码中有对应的 struct**：不得出现「配置文件里有但代码不读取」的情况
4. **新配置项必须同步更新**：
   - 代码中的 config struct
   - 文档中的配置说明
   - docker-compose.yml 中的 environment
5. **开发/生产配置分离**：`dev_mode: true` 控制开发行为（如前端反代、详细日志）
