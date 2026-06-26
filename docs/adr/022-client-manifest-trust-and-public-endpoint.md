# ADR-022: 客户端 OTA 信任模型——manifest 签名 + per-channel 拉取密钥 + 面向玩家公网拉取端点

- **日期**: 2026-06-23
- **状态**: accepted

## 上下文

客户端分发线（[ADR-021](021-client-distribution-jvm-updater.md)）要求玩家侧 updater-core 从 JM 拉取「版本清单（manifest）+ 内容文件」。这引入两个 JM 此前没有的东西：

- **一类面向玩家的公网入口**：JM 既有架构不变量是「Control Plane 是唯一面向浏览器的 HTTP 入口」，面向的是**运营者浏览器**（有账号、JWT 登录）。而客户端 OTA 端点面向的是**玩家机器上的 updater**（无账号、规模大、公网可达），是一类语义不同的新入口。
- **信任与防投毒诉求**：updater 在玩家机器上有完整文件系统写权限（替换 mods / runtime）。一旦下发通道被中间人 / CDN 投毒，等于向全体玩家机器推任意文件。信任模型必须从第一天就立住。

关键现实：**拉取密钥会随整合包分发到玩家机器，必然「半公开」**——能拿到整包的人就有 key。因此 key 不能承担「防篡改」职责。

## 决策

1. **per-channel 拉取密钥 = 鉴权路由 + 吊销，不是防篡改**：每个分发频道（channel，每服一个）一把拉取密钥；updater 拉 manifest / 制品时经请求头携带。它的作用是 ①区分频道并路由 ②泄露 / 滥用时可吊销轮换 ③挡未授权外人爬 CDN。**明示其为半公开凭据**，不作为内容可信的依据。密钥**落库只存 SHA-256 哈希**，明文仅创建 / 轮换时一次性返回、不可二次读取（同构 Beacon FR-42 / JM 既有运行时密钥惯例）。

2. **manifest 服务端私钥签名 = 信任根**：每份 manifest 由 JM 用私钥签名（Ed25519 或等价）；updater-core **内置对应公钥**，拉到 manifest 先验签，签名缺失 / 不符一律拒绝。即使 CDN / 中间人被攻破、文件被替换，没有私钥就伪造不出合法 manifest，客户端拒绝执行。**防投毒全靠签名，不靠 key、不靠传输层。**

3. **哈希分两用**：manifest 内每个文件同时带 `sha256`（信任校验）与 `md5` / `size`（本地快筛）。updater **本地比对增量可用 md5 / size 快筛**（性能），但**下载后的信任校验必须 sha256**；`md5` 已知可构造碰撞，**绝不作为信任依据**。

4. **新增「面向玩家公网分发端点」作为 CP 的第二类入口**：`/client-channels/:id/manifest`、`/client-artifacts/:sha256` 等端点经**拉取密钥**鉴权（非 JWT 浏览器登录），与运营者浏览器入口在鉴权与语义上区分。`.claude/rules/architecture-invariants.md` 与 `docs/ARCHITECTURE.md` 在 FR-087 实现时同步增补这一类入口（现阶段仅本 ADR 记录决策，端点未实现前不改不变量正文，避免文档超前于代码）。

5. **复用 FR-045 制品库做内容分发**：客户端文件作为制品库新类型 `type=client-file`，内容寻址（sha256）落 `var/artifacts/client-file/...`、去重；分发 url 可配 CDN base 回源。制品库的完整性校验（入库即算 sha256 / md5）与 OTA 校验链一致。

6. **基础包首发渠道可信**：含楔子 + 内置公钥的基础整包，首发经 HTTPS 下载 + 公示哈希；公钥随楔子分发即固化信任根（整包被篡改属首发渠道问题，由运营方保证，不在 OTA 通道范围）。

7. **防降级 / 重放攻击**：manifest 携带**单调递增版本号**，签名**覆盖 `version` 与文件全集**；updater 记住已见最高 `version`，**拒绝任何低于它的 manifest**——攻击者重放旧的、合法签名的 manifest 无法把客户端打回旧（可能有漏洞的）版本。运营回滚不通过「下发更低版本号」实现，而是**以更高版本号重发旧内容为新 latest**（FR-088），保持版本单调。

8. **签名密钥轮换 / 泄露应急**：updater **预埋主 + 备多个公钥（公钥版本化）**；私钥泄露时切到备用密钥对、并经一次基础包更新淘汰旧公钥，避免「公钥编在 updater 里、换公钥又依赖 updater 自更新（而自更新本身靠签名验证）」的鸡生蛋死锁。私钥服务端持有、env 注入不入库（理想离线 / HSM 签发），不随制品/CDN 分发。

## 理由

- **key 必然泄露，签名才防篡改**：拉取密钥随整包到玩家机器，把强安全压在它身上是错配；签名让信任独立于传输与 key。
- **md5 不可用于信任**：碰撞可构造，攻击者能造出同 md5 的恶意文件；md5 仅留作快筛。
- **公网入口需显式建模**：玩家侧 updater 与运营者浏览器是两类主体（鉴权、规模、暴露面都不同），混为一类会侵蚀「CP 唯一浏览器入口」不变量的清晰性，故显式新增一类入口并在不变量中登记。
- **复用制品库**：内容寻址 / 去重 / 校验已是 FR-045 能力，OTA 分发是其自然延伸，不另起存储。

## 后果

- 新增公网端点：`GET /client-channels/:id/manifest`（密钥鉴权、返回签名 manifest）、`GET /client-artifacts/:sha256`（内容分发、支持 Range）、频道 / 密钥 / 版本管理端点（FR-086 / 088）。
- `architecture-invariants.md` 与 `ARCHITECTURE.md` 在 FR-087 落地时增补「面向玩家公网分发端点（拉取密钥鉴权）」为 CP 的第二类入口，明确其不持浏览器会话、只读分发、与运营入口隔离。
- JM 需管理一对**签名密钥**（私钥服务端持有、不入库随环境注入；公钥编入 updater）；密钥轮换策略为后续事项（首期固定一对）。
- 制品库扩展 `type=client-file`；DB 频道 / 密钥 / 版本表新增（密钥只存哈希）。
- **关联 FR**：FR-086（频道 + 密钥）、FR-087（签名 manifest 端点 + 制品分发）、FR-088（版本编排 + 回滚指针）；客户端消费方 FR-090 / 091。

## 替代方案

- **只用拉取密钥、不签名 manifest** — key 半公开会泄露，泄露即可投毒，否决。
- **只用 md5 校验文件** — md5 碰撞可构造，等于给投毒留门，否决；md5 仅作快筛。
- **复用运营者浏览器 JWT 鉴权** — 玩家无账号、规模与暴露面不同、语义不符，否决。
- **mTLS 客户端证书认证 updater** — 证书同样随整包分发会泄露、且对玩家侧过重，否决。
- **传输层（HTTPS）即信任** — HTTPS 防不了源端 / CDN 被投毒、也防不了 key 泄露后的滥用，必须有应用层签名，否决「仅靠 HTTPS」。

## 实施补充（2026-06-27，签名密钥 fail-closed 强化）

决策 2/8 的安全前提是「私钥服务端持有、env 注入、不公开、不随分发」。FR-087 初版实现存在 **fail-open** 缺口：生产态（`dev_mode=false`）未注入 `JIANMANAGER_CLIENT_SIGN_PRIVKEY` 时，CP 静默回退到**源码中公开**的内置开发私钥继续对外签 manifest，仅打一条 `slog.Warn`。这等于把信任根私钥公开——攻击者可用人人可得的开发私钥伪造玩家客户端信任的 OTA manifest（供应链 / RCE），直接击穿决策 2 的信任根。

据本 ADR 的信任模型把密钥来源裁决改为 **fail-closed**（`service.ResolveManifestSigner(privKey, keyID, devMode)`）：

- `dev_mode=false`（生产）未注入私钥 → **拒绝启动**（`ErrSignKeyRequiredInProd`），绝不回退内置开发密钥；
- `dev_mode=false` 即便把源码公开的内置开发密钥**显式贴进 env**（运维误用），也按**解出的公钥**识别并**拒绝启动**（`ErrDevSignKeyInProd`，非字符串比对，防再编码绕过）；
- `dev_mode=true` 维持零配置回退内置开发密钥（公钥已回填 updater-core），仅供开发。

本补充**不改变也不取代**本 ADR 的任何决策，仅落实其既有安全前提、堵住实现层的 fail-open；故无新增 / superseded ADR。覆盖测试见 `internal/controlplane/service/client_manifest_test.go` 的 `TestResolveManifestSigner_*`。
