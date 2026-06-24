# ADR-026: 业务对接采用「适配器 + manifest 能力发现」而非插件实现 SPI

- **日期**: 2026-06-24
- **状态**: accepted（骨架；实现细节随 FR-118 落地）
- **上下文**: JBIS 要让核心链路（CP/Worker/桥/DB/UI）在接入越来越多业务插件时维护成本不爆（O(1) 而非 O(N)）。设计总纲 §1.3/§6 曾倾向"范式 B：JM 定 SDK 标准、业务插件各自实现 JBIS SPI 并经 ServicesManager 注册"。但调研两个真实插件（MultiCurrencyEconomy / AllinInventorySync，见 `.tmp/business-integration-plugin-contracts.md`）后发现：它们各有成熟自有 API，且 SDK 契约在未经真插件验证前就固化风险高（接 2 个真插件才知道标准该长什么样）。

## 决策

**第一批走范式 A：探针侧 per-plugin 薄适配器(Provider) wrap 插件 native API；核心链路只认 `domain + action + payload信封 + dedupKey`，插件无关。**

1. **唯一认识具体插件的地方 = 探针 platform 层的 per-plugin 适配器**：把通用动作（`economy.add` / `inventory.give`）翻译成插件真实 API 调用（`MultiCurrencyEconomyService` / 扩展后的 AllinInventorySync api）。脏活/强耦合/平台依赖全锁这一层。
2. **每个适配器自带 manifest 能力清单**：声明域、支持的动作、字段 schema、能力可用性。JM 靠 manifest **动态发现能力、动态校验、动态渲染 UI**——接第 N 个插件 = 写第 N 个适配器 + manifest，core 链路一行不改。
3. **服务发现经 Bukkit ServicesManager**（经济）或插件自有 Provider（背包），各适配器 `isReady`/降级自管。
4. **范式 B（插件实现 JBIS SPI）留 future**：JBIS 标准先以"适配器向上对 BusinessHost/桥的 domain+envelope+manifest 契约"形态存在并经两域验证；成熟后再提炼为开放 SPI 让第三方插件实现。

## 理由
- 最快验证整条链路（用两个真插件），SPI 不拍脑袋固化。
- 核心链路从第一天就插件无关（manifest 驱动），O(1) 接入即刻成立。
- 不要求改造业务插件即可接入（经济零改；背包仅扩自有 api 的写门面）。

## 后果
- 新增插件需写适配器 + manifest（而非插件自己实现 SPI）；闭源/改不动的插件天然用此垫片路线。
- manifest 是 JM 动态发现/校验/渲染的契约面，需定其 schema（随 FR-117/118 落地）。
- 修正设计总纲 §6 的范式 B 倾向（其建议 ADR-025「标准+SDK 依赖反转」/ADR-026「SDK 单一共享 jar 类加载」不采纳，合并为本 ADR 的适配器路线）。

## 关系
- **ADR-025**：适配器住 ServerProbe platform 层、事故域隔离。
- **ADR-027**：适配器向上经桥 `domain.verb` + 信封对话。
- **设计总纲** `docs/specs/business-integration/design.md` §1.3/§6（修正其范式 B 倾向）。
