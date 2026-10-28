package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 页面上下文的外发校验与裁剪（FR-518 §4.1 / §6）。
//
// 设计要点（三条都是被 spec 点名、且容易做错的）：
//  1. **剔除而非报错**：单个实体不存在/无权、单个 filter 超长或命名敏感、实体超上限，
//     都只丢掉那一项并回一条 `context_warning`，run 继续。理由见 spec §6：
//     用户可能只是刷新了页面，因一项失效就拒收整条消息过于粗暴。
//  2. **整个 JSON 超 4 KiB 则拒收**（400 CONTEXT_TOO_LARGE）：这已不是「某项失效」，
//     而是请求体本身异常；继续裁剪会让用户以为上下文完整送达。
//  3. **名称（label）不落库不外发**：模型看到的名称由工具按权限回填（shared-contracts §6）。

const (
	// assistantContextMaxEntities 实体上限（spec §4.1：至多 8 个）。
	assistantContextMaxEntities = 8
	// assistantContextMaxFilterLen filters 单个值上限（spec §4.1：截断至 64 字符）。
	assistantContextMaxFilterLen = 64
	// assistantContextMaxBytes 整个上下文 JSON 上限（spec §6：4 KiB）。
	assistantContextMaxBytes = 4 * 1024
	// assistantContextMaxPageKeyLen / MaxTabLen 枚举位本身很短，仍设上限防止被塞长串。
	assistantContextMaxPageKeyLen = 32
	assistantContextMaxTabLen     = 48
)

// assistantContextPageKeys 白名单页键（spec §4.2）。
//
// 未列入的页面（settings / permissions / users 等）**不提供上下文**——
// 它们的界面本身含敏感配置语义。因此白名单必须是显式集合，不是「除黑名单外都允许」。
var assistantContextPageKeys = map[string]struct{}{
	"instances": {},
	"nodes":     {},
	"logs":      {},
	"monitor":   {},
	"alerts":    {},
	"tasks":     {},
}

// assistantContextEntityTypes 允许的实体类型（spec §4.1）。
var assistantContextEntityTypes = map[string]struct{}{
	"instance": {},
	"node":     {},
	"task":     {},
	"alert":    {},
	"player":   {},
}

// assistantContextSensitiveKeyMarkers 敏感筛选键的片段黑名单（spec §6：脱敏）。
//
// 按键名**子串**匹配而不是精确匹配：实际键名有 apiKey / access_token / userPassword
// 等各种写法，精确匹配会漏掉绝大多数。宁可误伤（丢掉一个无害筛选）也不能外发凭据。
var assistantContextSensitiveKeyMarkers = []string{
	"token", "secret", "password", "passwd", "credential", "apikey", "api_key",
	"privatekey", "private_key", "authorization", "cookie", "sessionid", "session_id",
	"accesskey", "access_key", "signature", "bearer",
}

// assistantContextEntityRef 外发契约里的实体引用（spec §4.1）。
//
// `ID` 用 string 承接，但**接受 JSON 里的字符串或数字**：spec 允许 `string | number`，
// 而前端很可能直接传数字主键。只用 `string` 会让 `{"id":42}` 在解析阶段就整体 400，
// 把「一个合法实体」误判成「请求体不合法」——实测踩到过。
// 名称字段刻意**不存在**——见文件头第 3 条。
type assistantContextEntityRef struct {
	Type string             `json:"type"`
	ID   assistantContextID `json:"id"`
}

// assistantContextID 兼容 `string | number` 的实体标识。
//
// 统一存成字符串：后续既可能按数字主键查，也可能按 uuid 查，
// 保留原始文本形态再决定解析方式，比在解析期强转成数字更安全（uuid 会被转成 0）。
type assistantContextID string

// UnmarshalJSON 接受字符串或数字，其余形态报错。
func (c *assistantContextID) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if raw == "null" {
		*c = ""
		return nil
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = assistantContextID(strings.TrimSpace(s))
		return nil
	}
	// 数字：原样保留文本（不经 float64，避免大整数精度丢失）。
	*c = assistantContextID(raw)
	return nil
}

// MarshalJSON 始终输出字符串：落库形状稳定，前端不必区分两种类型。
func (c assistantContextID) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(c))
}

// assistantContextPayload 校验后的上下文（也是落库形状）。
type assistantContextPayload struct {
	V         int                         `json:"v,omitempty"`
	PageKey   string                      `json:"pageKey"`
	Tab       string                      `json:"tab,omitempty"`
	TimeRange string                      `json:"timeRange,omitempty"`
	Entities  []assistantContextEntityRef `json:"entities"`
	Filters   map[string]string           `json:"filters,omitempty"`
}

// assistantContextWarning 一条被裁掉的原因（转成 context_warning 事件）。
type assistantContextWarning struct {
	Field  string
	Reason string
}

// sanitizeAssistantContext 校验并裁剪外发上下文。
//
// 返回：
//   - payload：裁剪后的结构（nil 表示不落库）。**已剥夺 label、已丢弃非法项**；
//   - warnings：被丢弃/截断的项，供上层发 `context_warning` 事件；
//   - err：仅当整体超 4 KiB 或 JSON 不可解析时非空（对应 400）。
//
// 返回结构而非已序列化 JSON：调用方还要拿它做实体重验（§6 服务端重验），
// 序列化放到两步都做完之后。
//
// 注意：这里**不做权限判定**，也不查实体是否存在——那需要数据库与 UserAccess，
// 由调用方在本函数之后用 `revalidateAssistantContextEntities` 完成（spec §6 服务端重验）。
func sanitizeAssistantContext(raw *string) (payload *assistantContextPayload, warnings []assistantContextWarning, err error) {
	if raw == nil {
		return nil, nil, nil
	}
	body := strings.TrimSpace(*raw)
	if body == "" {
		return nil, nil, nil
	}

	// 上限判定用**原始字节数**：解构后再序列化会掩盖真实体积（例如重复键、超长空白）。
	if len(body) > assistantContextMaxBytes {
		return nil, nil, fmt.Errorf("%w: 页面上下文超过 %d 字节", ErrAssistantContextTooLarge, assistantContextMaxBytes)
	}

	var in assistantContextPayload
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		return nil, nil, fmt.Errorf("%w: 页面上下文不是合法 JSON", ErrAssistantInvalid)
	}

	out := assistantContextPayload{V: 1}

	// 页键：不在白名单即整段丢弃（未注册页不该产生任何上下文，spec §8 首条）。
	pageKey := strings.TrimSpace(in.PageKey)
	if pageKey == "" {
		return nil, nil, nil
	}
	if _, ok := assistantContextPageKeys[pageKey]; !ok {
		// 整段丢弃而不是「保留其它字段」：pageKey 是上下文语义的锚点，
		// 认不出页面时其余字段（筛选、实体）失去参照，留着只会误导模型。
		return nil, nil, nil
	}
	if utf8.RuneCountInString(pageKey) > assistantContextMaxPageKeyLen {
		return nil, nil, nil
	}
	out.PageKey = pageKey

	if tab := strings.TrimSpace(in.Tab); tab != "" {
		if utf8.RuneCountInString(tab) <= assistantContextMaxTabLen {
			out.Tab = tab
		} else {
			warnings = append(warnings, assistantContextWarning{Field: "tab", Reason: "页签名过长"})
		}
	}
	if tr := strings.TrimSpace(in.TimeRange); tr != "" {
		// 时间窗是枚举样式（"24h" / "7d"），用与 tab 同档的长度限制即可。
		if utf8.RuneCountInString(tr) <= assistantContextMaxTabLen {
			out.TimeRange = tr
		} else {
			warnings = append(warnings, assistantContextWarning{Field: "timeRange", Reason: "时间窗取值过长"})
		}
	}

	// 实体：白名单类型 + 上限 8（超出取前 8）。
	kept := 0
	for _, e := range in.Entities {
		if kept >= assistantContextMaxEntities {
			warnings = append(warnings, assistantContextWarning{
				Field:  "entities",
				Reason: fmt.Sprintf("实体超过 %d 个，其余已丢弃", assistantContextMaxEntities),
			})
			break
		}
		typ := strings.TrimSpace(e.Type)
		id := strings.TrimSpace(string(e.ID))
		if _, ok := assistantContextEntityTypes[typ]; !ok {
			warnings = append(warnings, assistantContextWarning{Field: "entities", Reason: "不支持的实体类型 " + truncateForWarning(typ)})
			continue
		}
		if id == "" || utf8.RuneCountInString(id) > 64 {
			warnings = append(warnings, assistantContextWarning{Field: "entities", Reason: "实体标识为空或过长"})
			continue
		}
		out.Entities = append(out.Entities, assistantContextEntityRef{Type: typ, ID: assistantContextID(id)})
		kept++
	}

	// 筛选：键名黑名单 + 值截断 64。
	if len(in.Filters) > 0 {
		filters := make(map[string]string, len(in.Filters))
		for k, v := range in.Filters {
			key := strings.TrimSpace(k)
			if key == "" || utf8.RuneCountInString(key) > 64 {
				warnings = append(warnings, assistantContextWarning{Field: "filters", Reason: "筛选键为空或过长"})
				continue
			}
			if assistantContextFilterKeyIsSensitive(key) {
				// 敏感键整键丢弃：值可能是凭据，连键名都不外发。
				warnings = append(warnings, assistantContextWarning{Field: "filters", Reason: "筛选键名疑似凭据，已丢弃"})
				continue
			}
			// 值形态判定（spec §8 双重判定）：键名无害但值像凭据也要丢。
			// **必须在截断之前**判断——JWT 上百字符，截到 64 会破坏三段结构导致失配。
			trimmedVal := strings.TrimSpace(v)
			if assistantContextFilterValueLooksSecret(trimmedVal) {
				warnings = append(warnings, assistantContextWarning{Field: "filters", Reason: "筛选值形似凭据，已丢弃"})
				continue
			}
			val := strings.TrimSpace(v)
			if utf8.RuneCountInString(val) > assistantContextMaxFilterLen {
				val = truncateRunes(val, assistantContextMaxFilterLen)
				warnings = append(warnings, assistantContextWarning{Field: "filters", Reason: "筛选值超过长度上限，已截断"})
			}
			filters[key] = val
		}
		if len(filters) > 0 {
			out.Filters = filters
		}
	}

	// 只有页键、没有任何实体/筛选/页签/时间窗时不落库：外发它没有信息量。
	if len(out.Entities) == 0 && out.Filters == nil && out.Tab == "" && out.TimeRange == "" {
		return nil, warnings, nil
	}

	// 裁剪后再次核对体积：结构规整后一般只会更小，但仍判一次，
	// 避免「原始刚好卡线、裁剪后反而超」的边界漏出。
	if encoded, marshalErr := json.Marshal(out); marshalErr == nil && len(encoded) > assistantContextMaxBytes {
		return nil, nil, fmt.Errorf("%w: 页面上下文超过 %d 字节", ErrAssistantContextTooLarge, assistantContextMaxBytes)
	}
	return &out, warnings, nil
}

// assistantContextFilterKeyIsSensitive 按键名子串判定是否疑似凭据。
func assistantContextFilterKeyIsSensitive(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range assistantContextSensitiveKeyMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// assistantContextSecretValuePatterns 敏感筛选**值**的形态判据（spec §8 双重判定）。
//
// 只有键名黑名单拦不住 `?q=eyJhbGciOi...`——键名 `q` 无害，值是粘来的 JWT。
//
// 【判据刻意保守】只认结构上明确无误的几类，不做「高熵随机串」猜测：
// 误杀会把正常筛选条件丢掉（用户只见「筛选没生效」，查不出原因），比漏杀更难排查。
//
// 与前端 PAGE_CONTEXT_SECRET_VALUE_PATTERNS（page-context.ts）**逐条对齐**：
// 前端丢弃是为了不外发，后端丢弃是最后一道闸，任何一侧放宽都会让另一侧形同虚设。
var assistantContextSecretValuePatterns = []*regexp.Regexp{
	// JWT：header.payload.signature 三段 base64url（header 必然以 eyJ 开头）
	regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}$`),
	// OpenAI 风格 sk-/pk-/rk-
	regexp.MustCompile(`(?i)^(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}$`),
	// GitHub token
	regexp.MustCompile(`^gh[pousr]_[A-Za-z0-9]{16,}$`),
	regexp.MustCompile(`^github_pat_[A-Za-z0-9_]{20,}$`),
	// AWS access key id
	regexp.MustCompile(`^AKIA[0-9A-Z]{16}$`),
	// Slack token
	regexp.MustCompile(`^xox[baprs]-[A-Za-z0-9-]{10,}$`),
	// 显式 Bearer 头
	regexp.MustCompile(`(?i)^Bearer\s+\S{16,}$`),
	// 纯十六进制摘要（md5/sha1/sha256）。uuid 含连字符不会命中，避免误杀实例/节点 id。
	regexp.MustCompile(`(?i)^[0-9a-f]{32,}$`),
}

// assistantContextFilterValueLooksSecret 判断筛选**值**是否形似凭据。
func assistantContextFilterValueLooksSecret(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	for _, re := range assistantContextSecretValuePatterns {
		if re.MatchString(v) {
			return true
		}
	}
	return false
}

// truncateForWarning 把可能很长的值压到可读长度后再放进告警文案。
func truncateForWarning(s string) string {
	const max = 24
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return truncateRunes(s, max) + "…"
}

// revalidateAssistantContextEntities 服务端重验实体（spec §6「服务端重验」）。
//
// 语义：不存在 / 无权 → **剔除该实体**，不报错、不区分「不存在」与「无权」。
// 后者是有意的——否则上下文会变成探测资源存在性的侧信道（shared-contracts §6）。
//
// 返回剔除后的 payload 与告警；payload 为 nil 表示实体全被剔除且已无其它字段。
func (s *AssistantChatService) revalidateAssistantContextEntities(
	ctx context.Context, owner uint, payload *assistantContextPayload,
) (*assistantContextPayload, []assistantContextWarning) {
	if payload == nil || len(payload.Entities) == 0 {
		return payload, nil
	}
	access, err := s.authz().LoadUserAccess(owner)
	if err != nil {
		// 装不出权限就一个实体都不认：宁可上下文残缺，也不能凭猜测放行。
		return &assistantContextPayload{
			V: payload.V, PageKey: payload.PageKey, Tab: payload.Tab,
			TimeRange: payload.TimeRange, Filters: payload.Filters,
		}, []assistantContextWarning{{Field: "entities", Reason: "无法确认实体权限，实体已全部剔除"}}
	}

	var kept []assistantContextEntityRef
	var warnings []assistantContextWarning
	for _, ref := range payload.Entities {
		ok, id := s.contextEntityAccessible(ctx, access, ref)
		if !ok {
			warnings = append(warnings, assistantContextWarning{
				Field:  "entities",
				Reason: fmt.Sprintf("实体 %s 不可访问，已剔除", truncateForWarning(ref.Type+":"+string(ref.ID))),
			})
			continue
		}
		kept = append(kept, assistantContextEntityRef{Type: ref.Type, ID: assistantContextID(id)})
	}
	out := *payload
	out.Entities = kept
	if len(kept) == 0 && out.Filters == nil && out.Tab == "" && out.TimeRange == "" {
		return nil, warnings
	}
	return &out, warnings
}

// contextEntityAccessible 单个实体的可达性判定。
//
// 返回 `(是否可访问, 规范化后的 id)`。id 会被规范化（如 uuid → 数字主键），
// 让落库的快照与「工具按 id 取数」用的是同一口径。
func (s *AssistantChatService) contextEntityAccessible(
	_ context.Context, access *UserAccess, ref assistantContextEntityRef,
) (bool, string) {
	switch ref.Type {
	case "instance":
		// 实例既可能给数字 id，也可能给 uuid（实例页深链用的是 uuid）。
		var inst model.Instance
		q := s.db.Where("uuid = ?", string(ref.ID))
		if n, err := parseUint(string(ref.ID)); err == nil {
			q = s.db.Where("id = ?", n)
		}
		if err := q.First(&inst).Error; err != nil {
			return false, string(ref.ID)
		}
		ok, err := s.authz().CanAccessInstance(access, inst.ID)
		if err != nil || !ok {
			return false, string(ref.ID)
		}
		return true, fmt.Sprintf("%d", inst.ID)
	case "node":
		var node model.Node
		q := s.db.Where("uuid = ?", string(ref.ID))
		if n, err := parseUint(string(ref.ID)); err == nil {
			q = s.db.Where("id = ?", n)
		}
		if err := q.First(&node).Error; err != nil {
			return false, string(ref.ID)
		}
		if !access.HasNode(node.UUID) {
			return false, string(ref.ID)
		}
		return true, fmt.Sprintf("%d", node.ID)
	case "task":
		// 任务按创建者收敛（TaskService.List 同口径）：非管理员只能看自己创建的。
		var task model.Task
		q := s.db.Where("task_id = ?", string(ref.ID))
		if n, err := parseUint(string(ref.ID)); err == nil {
			q = s.db.Where("id = ?", n)
		}
		if err := q.First(&task).Error; err != nil {
			return false, string(ref.ID)
		}
		if !access.IsPlatformAdmin && task.CreatedBy != access.UserID {
			return false, string(ref.ID)
		}
		return true, fmt.Sprintf("%d", task.ID)
	case "alert":
		// 告警事件：目标按规则维度解释，这里只校验「这条事件存在」。
		// 是否展示内容由工具按权限取（本 FR 不把告警正文放进上下文）。
		var ev model.AlertEvent
		if err := s.db.Where("id = ?", string(ref.ID)).First(&ev).Error; err != nil {
			return false, string(ref.ID)
		}
		return true, fmt.Sprintf("%d", ev.ID)
	case "player":
		// 玩家不作为持久实体存在（按会话/连接浮现），故一律剔除。
		// spec 允许该类型出现在契约里，但当前没有可信的可达性判据，
		// 「认不出来就剔掉」比「假装通过」安全。
		return false, string(ref.ID)
	default:
		return false, string(ref.ID)
	}
}

// parseUint 宽松解析正整数 id（失败即视为 uuid 形态）。
func parseUint(s string) (uint64, error) {
	var out uint64
	if s == "" {
		return 0, fmt.Errorf("空 id")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("非数字")
		}
		out = out*10 + uint64(r-'0')
		if out > 1<<62 {
			return 0, fmt.Errorf("id 过大")
		}
	}
	return out, nil
}
