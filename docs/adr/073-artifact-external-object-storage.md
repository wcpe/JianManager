# ADR-073: 制品库外置对象存储与 302 预签名分发

- **日期**: 2026-07-17
- **状态**: accepted（修订 ADR-011 存储节：「资产存 var/artifacts CAS」从**唯一物理落点**修订为**默认后端 + 键规范**——CAS 相对路径升格为跨后端存储键，client-file 类型可按活跃渠道落 S3 兼容对象存储；类型分区/sha256 去重/索引模型不变）
- **上下文**: FR-347。客户端分发制品（client-file）动辄数 GB，全落 CP 数据根且由 CP `ServeContent` 直出，CP 磁盘与出口带宽成分发瓶颈。用户要把制品上传自部署 rustfs（S3 兼容）做数据分发，CP 不存大对象。需要决策：下载分发形态、渠道与凭证建模、S3 客户端的依赖方向、既有消费方（updater/浏览器面板）的兼容边界。

## 决策

1. **S3 制品下载走 302 预签名短时效 URL，本地制品仍 CP 直出**。玩家消费端点 `GET /client-artifacts/:sha256` 在鉴权/防护/限流全部通过后按 `Asset.StorageBackend` 分流：local → `http.ServeContent`（原文不动）；s3 → 现算 SigV4 query 预签名 URL（TTL 渠道可配，默认 600s，钳 [60,3600]）回 `302 + Cache-Control: no-store`。CP 只当**授权与调度面**，字节流量走 rustfs 出口。拒绝的替代：CP 代理中继（带宽仍过 CP，外置失去意义）、S3 桶公开读（失去拉取密钥鉴权与吊销能力）、长效签名 URL（泄露即长期直连洞）。

2. **制品库独立一套存储渠道（`artifact_storage_channels`），不复用备份域 BackupStorage**。两域生命周期与消费方完全不同：备份配置由 Worker 消费（gRPC 下发、节点侧执行），制品渠道由 CP 自身消费（CP 上传/签名）；备份凭证走 `${ENV_VAR}` 引用（服务器侧运维语义），制品渠道面向面板运营直填。硬揉一张表 = 两组互斥字段 + 两套校验分支。渠道建模要点：
   - **内置「本机存储」行**（Builtin+local）：幂等 seed、不可删不可编辑、兜底活跃——local 恒可用的语义有 DB 实体承载，前端「设活跃」交互统一（无需「关闭外置」特例开关）。
   - **单活跃渠道**：`Active` 布尔 + service 事务「先清后设」，写路径唯一路由开关；活跃渠道禁删（先切走）。切活跃只影响新上传，存量 Asset 按自身 `StorageBackend + StorageChannelID` 自述读取——**位置由记录自述，不由全局状态推断**，这是迁移（FR-348）与对账（FR-349）能安全建立的地基。
   - **仅 client-file 走渠道路由**：其余类型（core/plugin/updater-core 等）是 CP/Worker 运行链路的本地依赖（provision 拷贝、内嵌归档、classpath 引用），外置只添故障面无分发收益。

3. **S3 客户端 CP 侧独立实现（`internal/controlplane/blobstore`），不 import `internal/worker/storage`，也不此刻抽中立共享包**。CP import worker 包违反进程边界语义（`.claude/rules/architecture-invariants.md` 依赖方向）；把 worker 的 s3 后端迁到中立包则要动备份域正在生产运行的代码，超出本 FR 范围且两域演进节奏不同（备份域按 Worker 下发 spec 消费，制品域要 presign/Stat/List worker 版没有）。SigV4 核心 ~150 行纯标准库代码，重复成本低于耦合成本；CP 版以 **AWS 官方签名向量**（presign 文档例：`AKIAIOSFODNN7EXAMPLE` / examplebucket / 20130524，期望签名 `aeeed9bb…`）逐字节对拍保正确性。出现第三个消费方时再抽 `internal/platform/objectstore` 统一（届时取代本条）。

4. **凭证面板直填 + AES-256-GCM 可逆加密落库，复用 FR-192 `KeyEncryptor` 基建（含 FR-263 三轨密钥来源），不自造加密**。与备份域 `${ENV_VAR}` 形态分道的理由：制品渠道是运营在面板闭环配置的对象（同拉取密钥的信任级与操作面），要求先 SSH 设环境变量再回面板填引用，对目标用户（中小运营商）是断裂体验；CP 需要随时解密出 SK 现算预签名（env 引用能力上等价，安全上并不更强——两者的信任根都是 CP 进程环境）。约束：API 响应**永不含**明文或密文（列以 `HasAccessKey/HasSecretKey` 示意）；编辑留空=保留原值；加密器未配置（生产 autogen 失败降级态）时创建/编辑 s3 渠道 422 快失败——绝不落明文。

5. **写路径快失败，不静默回落**。活跃=s3 时上传失败直接报错（运营立即可见），拒绝「S3 失败自动落回本地」：静默回落让 blob 落点不可预期、与「CP 不存大对象」的用户意图相悖，且失败被吞会推迟发现渠道故障。去重命中（同 type+sha256）不迁移不重传，复用既有记录（内容寻址同内容等价）。

6. **消费方分野：updater 吃 302，浏览器面板走 CP 代理**。管理面下载现实现为 axios blob fetch（JWT 端点），302 到跨域预签名 URL 会撞 rustfs CORS（默认无 CORS 头，浏览器跟随后拦响应）；管理面下载/预览是低频运维动作，CP 经 BlobStore.Open 代理直流成本可忽略、前端零改动、rustfs 无需配 CORS。**updater 跨协议限制**：Java `HttpURLConnection` 开了 `setInstanceFollowRedirects(true)` 也**不跨 http↔https 跟随**——部署约束（文档级）：CP 与 S3 endpoint 同协议（CP https 则 rustfs https/同域反代；内网全 http 亦可）。不改 updater（动客户端发版链路，YAGNI）。

## 理由

- 302 预签名是对象存储分发的行业标准形态：鉴权留在应用（拉取密钥语义完整保留，403/吊销/限流照常），字节走存储出口；短 TTL + `no-store` 把 URL 泄露窗口压到分钟级。
- 存储位置「记录自述」（StorageBackend + StorageChannelID 落在 Asset 行上）而非「全局推断」，使切渠道、迁移、混合分布都不产生歧义读路径。
- 复用 KeyEncryptor：一套主密钥三轨来源（env > 生产 autogen > dev 回退）已被 FR-192/248/263 验证，密钥运维口径一致（同一份 `etc/client-key-enc.key` 的备份策略覆盖新用途）。
- PutFile（收临时文件路径）而非 Put(io.Reader) 作为写接口：Ingest 恒先落临时文件算 hash，local 适配器得以用与主线逐字节相同的 `os.Rename` 保证零行为变化，S3 适配器拿到可重读文件做流式 PUT。

## 后果

- CP 与 S3 时钟偏移超 TTL 会使预签名 403：运维要求 NTP（真机验收含此点）。
- 加密主密钥文件丢失时存量渠道凭证不可解密，s3 上传/预签名快失败（报错可见，恢复=还原密钥文件或重填凭证）；与 FR-192 同一风险面，无新增敞口。
- updater 跟随 302 时会把 `X-Client-Key` 等头重放给 S3 host：预签名指向运营商自有存储、拉取密钥本半公开（ADR-022），无新增泄露面。
- 制品分发追踪（FR-249）中 s3 下载的 Bytes 记 CP 出口字节（≈0）：事件语义变为「已授权跳转」，S3 侧真实流量不经 CP 不可见；补齐观测归 FR-349 对账域。
- 备份域与制品域各持一份 SigV4 实现：第三个消费方出现时抽 `internal/platform/objectstore` 统一（本 ADR 决策 3 届时取代）。
