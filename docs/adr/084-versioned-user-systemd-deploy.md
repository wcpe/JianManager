# ADR-084：Linux 用户级 systemd 的版本化部署布局

- 日期：2026-08-25
- 状态：accepted
- 关联：FR-410、ADR-010、ADR-051、ADR-063、FR-277、FR-282

## 上下文

ADR-063 的 SSH 推送脚本将二进制直接放在安装根，更新时仅留下一个 `.bak`。这不能保留多个历史版本，也无法把当前运行版本和稳定的数据、配置、Worker 身份清晰分离。真实环境还存在同机多个 Worker，其 unit 名必须与安装目录一致。

本期仅补 Linux 普通用户的 systemd user unit 部署。不建立公开镜像、Docker Compose 生产部署、Kubernetes 或系统级 systemd 的新部署模型。

## 决策

1. 在 `JM_SERVICE_SCOPE=user` 下，CP 和 Worker 安装根固定采用：

   ```text
   <安装根>/
   ├── current -> versions/<版本>--<UTC 时间戳>--<sha12>/
   ├── versions/<不可变版本目录>/<二进制>
   ├── data/
   ├── control-plane.yml       # 仅 CP
   └── service.env             # 仅 CP，0600
   ```

2. 新版本目录标识取二进制 `--version`、部署瞬间 UTC 时间戳和二进制 SHA-256 前 12 位；若秒级名称碰撞，在末尾追加递增序号。目录另存完整摘要与安装时间的 manifest。迁移不支持 `--version` 的历史裸二进制时，用 `unknown` 版本段和 SHA-256 归档保留。部署永不覆盖或自动清理既有版本。
3. user unit 的 `ExecStart` 固定指向 `<安装根>/current/<二进制>`；`data`、CP 配置、CP JWT 环境文件、Worker `worker.yml` 与 `node-identity.json` 不进入版本目录。
4. 部署和回滚在同一文件系统内以临时符号链接加 `rename` 原子切换 `current`，再重启相应 user unit。若新版本未通过服务健康检查，脚本恢复旧 `current` 并重启旧版本；数据库迁移不可逆时不承诺数据 schema 回滚。
5. 首次新版部署检测旧裸二进制和 `.bak`，将它们各自归档为版本目录，然后改写 user unit 指向 `current`。配置、数据、Worker 身份均原地保留。
6. `deploy-cp.sh`、`deploy-worker.sh` 保持现有 SSH 推送入口。新增 `rollback-cp.sh <版本目录>`、`rollback-worker.sh <版本目录>`，使用相同 `JM_*` SSH 契约在远端切换指定版本。Worker 服务名以安装目录稳定派生，并由安装与更新路径共用同一契约，确保同机多节点不误操作。
7. CP 首次 user 部署在 `service.env` 缺失时生成非默认随机 JWT 密钥并以 0600 保存，unit 通过 `EnvironmentFile` 读取；后续部署绝不覆盖它。

## 后果

- 版本可审计、可显式回退，且不会因同名开发构建覆盖历史二进制。
- 运行态数据与配置不随回滚切换，符合 ADR-010 的稳定数据根原则；回滚的是程序二进制，不是数据库内容。
- `JM_SERVICE_SCOPE=system` 保持 ADR-063 既有行为，不纳入 FR-410 的版本化迁移范围。
- 必须为迁移、原子切换、服务名派生、JWT 首装和真实 user-unit 部署补测试与真机证据。
