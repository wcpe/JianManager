# ADR-061: CP↔Worker 专用 WS 令牌密钥与注册下发（修订 ADR-020）

- **日期**: 2026-07-10
- **状态**: accepted
- **修订**: [ADR-020](020-node-enrollment-and-deploy.md) §3「Worker 侧 enrollment 与凭据持久化」——注册响应下发的长期凭据从「仅 node_uuid/node_secret」扩展为「+ WS 令牌密钥」；ADR-020 其余立场不变。
- **关联**: ADR-016（插件桥 token 载体）、ADR-039（node-identity.json 身份文件）、ADR-043（心跳下发运行时配置先例）、ADR-051（worker setup 持久化路径）、ADR-052 / FR-263（密钥三轨 autogen 先例）、FR-275/276（本 ADR 落地）。

## 上下文

浏览器终端走 browser → CP(`/ws/terminal`) → Worker(`:wsPort/ws/terminal`) 双向桥接：CP 签发一次性终端 token（30s），Worker 独立校验同一 token。ServerProbe 插件桥（`/ws/plugin-bridge`，ADR-016）同构：CP 签发实例级长期 token（约 10 年 TTL，写入探针 config.yml），Worker 握手校验。二者的签发/校验密钥**必须两端一致**，但真机验收暴露三个叠加缺口：

1. **密钥从不下发**：enroll/一键安装（FR-080/222/223）不下发也不设置 Worker 的 `jwt_secret`，node-identity.json 不含它。生产 CP 设自定义 `JIANMANAGER_JWT_SECRET` 后，一键安装的 Worker 仍是默认 `dev-secret-change-me` → 终端 401「token 无效或已过期」、**插件桥监控一并失效**（Worker 单个 `cfg.JWTSecret` 同时喂 `TerminalServer` 与 `PluginBridgeServer`）。dev 因两端默认相同而侥幸可用，掩盖缺口。
2. **CP 侧 `jwt.secret` 是主签名密钥**：同一把密钥签用户登录/管理员会话（`middleware.JWTAuth`）+ 终端 token + 插件桥 token。若把它原样下发给 Worker，任一 Worker 沦陷即可**伪造 CP 管理员会话**——Worker 是暴露面更大的层级，不可接受。
3. **默认密钥即安全洞**：未同步密钥的生产 Worker 用源码公开的 `dev-secret-change-me` 校验 WS 令牌——任何能达 Worker WS 端口者可自签 `permission=write` 的终端 token，直接操纵游戏服 stdin。

## 决策

1. **专用 WS 令牌密钥，与用户会话密钥分离**。CP 新增独立密钥专签「CP↔Worker WS 令牌」（终端 token + 插件桥 token）；`jwt.secret` 回归只签用户会话。Worker 永不持有 `jwt.secret`。签发点切换：`TerminalService` / `TerminalProxy` / `PluginBridgeService`（探针 FR-068 在线更新的 token 重发经 `PluginBridgeService` 自动随切）；`AuthService` 与路由 JWT 中间件**不动**。

2. **CP 侧密钥来源三轨**（优先级由高到低，镜像 FR-263 `ResolveKeyEncryptor` 先例）：
   1. **显式配置**：`jwt.ws_secret`（env `JIANMANAGER_JWT_WS_SECRET`）非空即用之，不生成、不持久化——也是存量部署的**过渡逃生口**（可显式设回旧值保持与未升级 Worker 兼容）。
   2. **生产未配**（`dev_mode=false`）：自动生成 32 字节随机密钥（base64 串），持久化到 `<dataRoot>/etc/ws-token-secret.key`（0600，先写临时文件再原子 rename）；已存在则加载（跨重启稳定）。
   3. **dev 未配**：回退 `dev-secret-change-me`（与现状默认一致，保 dev 零配置连续性——存量 dev 部署、探针旧 token 全不受影响）。
   - **生成/读取失败 → fail-fast**（同 ADR-052 决策 2）：WS 密钥不可用则终端/监控必坏，静默降级只会掩盖问题；且不得回退 `jwt.secret`（重新引入缺口 2）。

3. **经 gRPC 注册响应下发 + 心跳携带**（不进命令行/脚本/配置文件）：
   - `RegisterResponse` 加 `ws_token_secret` 字段，**首注册（createNewNode）与重注册（reregisterExisting）均填充**——与 node_secret 同通道、同信任模型（ADR-020 §3「长期凭据走 gRPC 响应 + 0600 落盘」）。
   - `HeartbeatResponse` 加 `ws_token_secret` 字段每拍携带（镜像 ADR-043 代理下发）：Worker 比对变化才应用——CP 轮换密钥后 Worker **不重启即自愈**（≤1 心跳周期），消除「轮换后全网终端静默失效直到逐台重启」的运维坑。**仅对已鉴权流下发**：心跳对未出示 `node-secret` 的调用方跳过鉴权（FR-004 旧版兼容），若无门槛携带密钥，任何可达 CP gRPC 端口者开个心跳流即可白拿密钥、伪造终端写令牌——故密钥只填充给「首拍 node_secret 校验通过」的流；新版 Worker 心跳恒带 node-secret，不受影响。
   - 否决「写入一键安装命令/worker.yml」：长期密钥暴露在可复制命令、进程参数、shell 历史、systemd unit 中，违反 config-files「密钥不硬编码」与 ADR-020「一次性凭据不留盘、长期凭据走 gRPC」立场。

4. **Worker 持久化与回退链**：下发值持久化进 `etc/node-identity.json`（0600，ADR-039 载体）新字段 `wsTokenSecret`。生效优先级：**心跳/注册下发 > 身份文件持久化值 > 本地 `jwt_secret` 配置/默认**（后者保对旧 CP 的向后兼容：旧 CP 响应无该字段 → 空值 → 行为与现状完全一致）。启动时以身份文件值构造 WS 服务器（消除「注册完成前用旧密钥」窗口），注册/心跳后经 setter 热更新。

## 理由

- **最小信任面**：Worker 只拿到「伪造实例级 WS 令牌」能力的密钥；泄露爆炸半径从「CP 管理员会话」缩到「本节点终端/插件桥」。
- **零运维闭环**：生产态 autogen + 注册自动下发，一键安装的 Worker 开箱终端/监控可用，无需任何手工密钥同步——这正是 FR-080「傻瓜部署」缺的最后一块。
- **与既有范式同构**：三轨 autogen（ADR-052/FR-263）、gRPC 响应下发长期凭据（ADR-020 §3）、心跳下发运行时应用（ADR-043）、身份文件 0600（ADR-039）全是已验证过的既有模式，无新发明。
- **顺手关掉安全洞**：默认密钥校验 WS 令牌的生产 Worker（上下文 §3）在升级后自动换为随机密钥。

## 后果

- proto `RegisterResponse` / `HeartbeatResponse` 各加一个 string 字段（向后兼容，旧端忽略）。
- 数据根新增 `etc/ws-token-secret.key`（CP 侧）；`etc/node-identity.json`（Worker 侧）加 `wsTokenSecret` 字段。误删 CP 密钥文件 → 下次启动生成新密钥 → 已签发的探针长期 token 失效（见下条），随数据根整体备份即可（ADR-010）。
- **存量探针长期 token 迁移面**：插件桥 token（约 10 年 TTL）已写死在探针 config.yml。两端升级后 CP/Worker 换用新密钥 → 旧 token 在探针**下次重连**时 401，需重发探针配置（FR-068 探针在线更新 / 重建服）恢复。dev 不受影响（回退值与旧默认相同）。OPERATIONS 须标注。
- **升级顺序面**：新 CP + 旧 Worker（曾手动同步过 jwt_secret 的部署）→ CP 改用新密钥签发而旧 Worker 仍校验 jwt_secret → 终端断。缓解：随 FR-081 编排同批升级 Worker；或临时设 `jwt.ws_secret` = 旧 `jwt.secret` 过渡（逃生口）。未同步过的部署本就是坏的，升级即修复。
- CP↔Worker gRPC 当前明文（`insecure.NewCredentials()`），WS 密钥与 node_secret 同通道同风险级；传输加固（mTLS）仍是 ADR-020 已否决/留后续的独立议题，本 ADR 不扩大也不缩小该面。
- **下发路径的鉴权面与 node_secret 对齐**：注册三路径（enroll token 新建 / UUID+secret 重注册 / 同机 host 旧版兼容）均随响应下发——旧版兼容路径可被「猜对 name+host」骗取，但该路径本就返回 node_secret（ADR-020 有意的过渡权衡），持 node_secret 者亦可经已鉴权心跳取得 WS 密钥，故单独遮蔽它不增安全、只碎化行为；ADR-020 遗留的「彻底收口」FR 落地时二者一并关闭。心跳路径则如决策 3：仅已鉴权流下发。
- `.claude/rules/architecture-invariants.md` 增约束：CP↔Worker WS 令牌用专用共享密钥，与用户会话密钥（`jwt.secret`）隔离，后者永不下发 Worker。

## 替代方案

- **原样下发 `jwt.secret`**：改动最小，但 Worker 持有可伪造管理员会话的主密钥（上下文 §2），否决。
- **写入一键安装命令 / worker.yml**：密钥暴露在命令行/脚本/shell 历史/systemd unit，且轮换无自愈通道，否决（见决策 3）。
- **仅文档 + 401 诊断（不下发）**：把密钥同步留给运维手册，傻瓜部署承诺（FR-080）落空、默认密钥安全洞仍在，否决——诊断作为兜底另立 FR-276 与本 ADR 并行落地。
- **每节点独立 WS 密钥（per-node secret）**：CP 按节点签发不同密钥，泄露隔离更细。但终端 token 由 CP 统一签发、代理转发，须按目标节点选 key，签发/校验/轮换复杂度显著上升；单密钥已把爆炸半径缩到实例级 WS 令牌，YAGNI，留作未来增强（proto 字段与下发通道已就位，升级路径平滑）。
- **仅注册响应下发（无心跳携带）**：轮换后须逐台重启 Worker 才生效，静默失效窗口不可控；心跳携带成本极低（一个 string 字段 + 变化比对）且有 ADR-043 成熟先例，否决。
