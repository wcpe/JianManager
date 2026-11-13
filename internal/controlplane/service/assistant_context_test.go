package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/* *
 *§4.1 / §6 的上下文裁剪语义。
 *
 * 这里守的是两类**容易做反**的规则：
 *  1. 「剔除后继续」与「拒收」的边界：单实体/单 filter/实体超限 → 剔除 + 告警；
 *     整体超 4 KiB → 拒收（400）。混为一谈会让用户以为上下文完整送达。
 *  2. 名称不外发、敏感键丢弃：`label` 不该出现，键名含 token/secret 的整键丢掉。
 */
func rawContext(t *testing.T, v any) *string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化测试上下文失败: %v", err)
	}
	s := string(b)
	return &s
}

func TestSanitizeAssistantContext_NotWhitelistedPageDropped(t *testing.T) {
	// settings / permissions / users 这类页面本身含敏感配置语义，整段不产生上下文。
	s, warns, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey":  "settings",
		"entities": []map[string]any{{"type": "instance", "id": "1"}},
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s != nil {
		t.Fatalf("未注册页不该产生上下文，得到 %+v", s)
	}
	if len(warns) != 0 {
		t.Fatalf("整段丢弃不该产生逐项告警（避免噪声），得到 %v", warns)
	}
}

func TestSanitizeAssistantContext_OnlyPageKeyNotStored(t *testing.T) {
	// 只有 pageKey、没有实体/筛选/页签/时间窗 = 无信息量，不该落库。
	s, _, err := sanitizeAssistantContext(rawContext(t, map[string]any{"pageKey": "instances"}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s != nil {
		t.Fatalf("无信息量上下文不该落库，得到 %+v", s)
	}
}

func TestSanitizeAssistantContext_LabelNeverLeaks(t *testing.T) {
	// spec §6「名称丢弃」：`label` 只用于 UI，外发前必须移除。
	s, _, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey":  "instances",
		"entities": []map[string]any{{"type": "instance", "id": 42, "label": "survival-1"}},
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s == nil {
		t.Fatal("应产出上下文")
	}
	out, _ := json.Marshal(s)
	if strings.Contains(string(out), "survival-1") || strings.Contains(string(out), "label") {
		t.Fatalf("label 不该外发，得到 %s", out)
	}
}

func TestSanitizeAssistantContext_EntitiesCappedAt8(t *testing.T) {
	// 超上限保留前 8 个并告警（保护性约束，不必阻塞）。
	ents := make([]map[string]any, 0, 12)
	for i := 0; i < 12; i++ {
		ents = append(ents, map[string]any{"type": "instance", "id": i + 1})
	}
	s, warns, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey": "instances", "entities": ents,
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s == nil || len(s.Entities) != assistantContextMaxEntities {
		t.Fatalf("实体应保留 %d 个，得到 %+v", assistantContextMaxEntities, s)
	}
	if len(warns) == 0 {
		t.Fatal("超上限应产生告警")
	}
}

func TestSanitizeAssistantContext_SensitiveFilterKeysDropped(t *testing.T) {
	// 键名含 token/secret/password 等一律整键丢弃（值可能是凭据，键名也不外发）。
	s, warns, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey": "instances",
		"filters": map[string]string{
			"status":      "RUNNING",
			"apiKey":      "sk-xxx",
			"accessToken": "abc",
			"password":    "p@ss",
		},
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s == nil || s.Filters == nil {
		t.Fatal("应保留非敏感筛选")
	}
	if _, ok := s.Filters["status"]; !ok {
		t.Fatal("非敏感筛选应保留")
	}
	for _, k := range []string{"apiKey", "accessToken", "password"} {
		if _, ok := s.Filters[k]; ok {
			t.Fatalf("敏感键 %q 不应外发", k)
		}
	}
	if len(warns) < 3 {
		t.Fatalf("每个丢弃的敏感键都应有告警，得到 %v", warns)
	}
	// 值本身也不能以任何形式出现在序列化结果里。
	out, _ := json.Marshal(s)
	if strings.Contains(string(out), "sk-xxx") || strings.Contains(string(out), "p@ss") {
		t.Fatalf("凭据值不该外发，得到 %s", out)
	}
}

func TestSanitizeAssistantContext_FilterValueTruncated(t *testing.T) {
	// 用 "x" 而不是 "a"：全 a 是合法十六进制，会被「值形态判据」认作摘要而整键丢弃，
	// 那样测到的就不是截断行为，而是脱敏行为（本用例初版正因为换成 a 而变红）。
	long := strings.Repeat("x", assistantContextMaxFilterLen+20)
	s, warns, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey": "instances", "filters": map[string]string{"q": long},
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s == nil {
		t.Fatal("应保留筛选")
	}
	if got := len([]rune(s.Filters["q"])); got != assistantContextMaxFilterLen {
		t.Fatalf("筛选值应截断到 %d 字符，得到 %d", assistantContextMaxFilterLen, got)
	}
	if len(warns) == 0 {
		t.Fatal("截断应有告警")
	}
}

func TestSanitizeAssistantContext_TooLargeRejected(t *testing.T) {
	// 整体超 4 KiB 属请求体异常 → 拒收（不是裁剪后继续）。
	huge := strings.Repeat("x", assistantContextMaxBytes+1)
	_, _, err := sanitizeAssistantContext(&[]string{huge}[0])
	if !errors.Is(err, ErrAssistantContextTooLarge) {
		t.Fatalf("期望 ErrAssistantContextTooLarge，得到 %v", err)
	}
}

func TestSanitizeAssistantContext_InvalidJSONRejected(t *testing.T) {
	bad := "not json"
	_, _, err := sanitizeAssistantContext(&bad)
	if !errors.Is(err, ErrAssistantInvalid) {
		t.Fatalf("期望 ErrAssistantInvalid，得到 %v", err)
	}
}

func TestSanitizeAssistantContext_UnknownEntityTypeDropped(t *testing.T) {
	s, warns, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey": "instances",
		"entities": []map[string]any{
			{"type": "instance", "id": 1},
			{"type": "spaceship", "id": 2},
		},
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s == nil || len(s.Entities) != 1 || s.Entities[0].Type != "instance" {
		t.Fatalf("未知类型应被丢弃，得到 %+v", s)
	}
	if len(warns) == 0 {
		t.Fatal("丢弃未知类型应有告警")
	}
}

func TestSanitizeAssistantContext_PlayerTypeAcceptedButDroppedOnRevalidate(t *testing.T) {
	// 契约允许 player 出现（§4.1），但当前没有可信的可达性判据，
	// 重验时应一律剔除——「认不出来就剔掉」比「假装通过」安全。
	s, _, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey":  "monitor",
		"entities": []map[string]any{{"type": "player", "id": "steve"}},
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s == nil || len(s.Entities) != 1 {
		t.Fatalf("结构校验阶段应保留 player（类型在白名单内），得到 %+v", s)
	}
	if ok, _ := (&AssistantChatService{}).contextEntityAccessible(nil, &UserAccess{}, s.Entities[0]); ok {
		t.Fatal("player 重验阶段应一律不可达")
	}
}

func TestFilterKeySensitiveMatching(t *testing.T) {
	cases := map[string]bool{
		"status":        false,
		"q":             false,
		"apiKey":        true,
		"access_token":  true,
		"userPassword":  true,
		"Authorization": true,
		"credentialId":  true,
	}
	for key, want := range cases {
		if got := assistantContextFilterKeyIsSensitive(key); got != want {
			t.Fatalf("%q: 期望 %v 得到 %v", key, want, got)
		}
	}
}

func TestSanitizeAssistantContext_SensitiveFilterValuesDropped(t *testing.T) {
	// 值形态判定（spec §8 双重判定）：键名无害但值像凭据，同样要丢弃。
	// 这类外发只有第二条判据能拦——`q` 是完全正常的键名。
	jwt := "eyJhbGciOiJIUzI1NiJ9." + strings.Repeat("a", 40) + "." + strings.Repeat("b", 20)
	s, warns, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey": "instances",
		"filters": map[string]string{
			"status": "RUNNING",
			"q":      jwt,
			"q2":     "sk-abcdefghijklmnopqrstuvwxyz123456",
			"q3":     strings.Repeat("c", 64), // sha256 形态
		},
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s == nil || s.Filters == nil {
		t.Fatal("应保留非敏感筛选")
	}
	if _, ok := s.Filters["status"]; !ok {
		t.Fatal("普通筛选值应保留")
	}
	for _, k := range []string{"q", "q2", "q3"} {
		if _, ok := s.Filters[k]; ok {
			t.Fatalf("凭据形态的值 %q 不应外发", k)
		}
	}
	if len(warns) < 3 {
		t.Fatalf("每个被丢弃的凭据值都应有告警，得到 %v", warns)
	}
	// 值本身不得以任何形式出现在序列化结果里
	out, _ := json.Marshal(s)
	if strings.Contains(string(out), "eyJhbGciOiJIUzI1NiJ9") || strings.Contains(string(out), "sk-abcdefghij") {
		t.Fatalf("凭据值不该外发，得到 %s", out)
	}
}

func TestAssistantContextFilterValueLooksSecret_NoFalsePositive(t *testing.T) {
	// 不该拦的一律放行——误杀会让筛选静默失效，比漏杀更难排查。
	safe := []string{
		"RUNNING",                              // 状态枚举
		"survival-1",                           // 实例名
		"beacon-1",                             // 实例名
		"生产环境",                                 // 中文
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301", // uuid（含连字符，不匹配 hex 判据）
		"",                                     // 空值
		"2026-10-27",                           // 日期
	}
	for _, v := range safe {
		if assistantContextFilterValueLooksSecret(v) {
			t.Fatalf("%q 不该被判为凭据（误杀会让筛选静默失效）", v)
		}
	}

	secret := []string{
		"eyJhbGciOiJIUzI1NiJ9." + strings.Repeat("a", 40) + ".sig12345",
		"sk-abcdefghijklmnopqrstuvwxyz123456",
		"ghp_abcdefghijklmnopqrstuvwxyz123456",
		"AKIAIOSFODNN7EXAMPLE",
		"xoxb-123456789012-abcdefghijkl",
		strings.Repeat("d", 32),
		strings.Repeat("e", 40),
		strings.Repeat("f", 64),
	}
	for _, v := range secret {
		if !assistantContextFilterValueLooksSecret(v) {
			t.Fatalf("%q 应被判为凭据", v)
		}
	}
}

func TestSanitizeAssistantContext_ValueCheckBeforeTruncate(t *testing.T) {
	// 判定必须在截断之前：JWT 上百字符，先 slice 再匹配会破坏三段结构导致失配。
	// 这是「先裁剪再校验」的顺序错误，会让整个判据静默失效。
	longJwt := "eyJhbGciOiJIUzI1NiJ9." + strings.Repeat("a", 80) + "." + strings.Repeat("b", 40)
	if len(longJwt) <= assistantContextMaxFilterLen {
		t.Fatalf("构造的 JWT 应超过 %d 字符上限", assistantContextMaxFilterLen)
	}
	s, _, err := sanitizeAssistantContext(rawContext(t, map[string]any{
		"pageKey": "instances",
		"filters": map[string]string{"q": longJwt},
	}))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if s != nil && s.Filters != nil {
		if _, ok := s.Filters["q"]; ok {
			t.Fatalf("超长 JWT 仍须被丢弃，不能截断后当普通值外发，得到 %+v", s.Filters)
		}
	}
}

/* *
 * 服务端重验实体（§6 / §8「伪造实体 id 被剔除，run 继续，不返回存在性提示」）。
 *
 * 这里守两条**同生共死**的性质：
 *  1. 不存在 / 他人实例 → 该实体被**剔除**（不是整单拒绝，run 要能继续）；
 *  2. 剔除时**不暴露存在性**——返回值是布尔，调用方无法区分「不存在」与「无权限」。
 *     若哪天改成返回 error 并区分 `ErrNotFound` / `ErrForbidden`，上下文就成了探测接口。
 */
func TestAssistantContext_RevalidateRejectsForgedEntities(t *testing.T) {
	db := newAssistantChatTestDB(t)
	// 助手测试库只迁了会话/消息/run/事件四张表；实体重验要查 instances
	// （以及它关联的 groups 归属），故这里补迁，避免「表不存在」被误读成「实体不可达」。
	if err := db.AutoMigrate(&model.Instance{}, &model.Node{}, &model.Group{}); err != nil {
		t.Fatalf("补迁实例相关表失败: %v", err)
	}
	svc := NewAssistantChatService(db, nil)
	t.Cleanup(svc.Stop)

	// 造一台**未分配组**的实例：按 authz 规则，未分组实例仅平台管理员可访问。
	inst := &model.Instance{
		UUID:         "11111111-2222-3333-4444-555555555555",
		Name:         "someone-elses-instance",
		Status:       "RUNNING",
		NodeID:       1,
		Type:         "minecraft_java",
		Role:         "backend",
		ProcessType:  "direct",
		StartCommand: "java -jar server.jar",
	}
	if err := db.Create(inst).Error; err != nil {
		t.Fatalf("造实例失败: %v", err)
	}

	member := &UserAccess{UserID: 42, Role: model.RoleMember}
	admin := &UserAccess{UserID: 1, Role: model.RolePlatformAdmin, IsPlatformAdmin: true}

	cases := []struct {
		name   string
		access *UserAccess
		ref    assistantContextEntityRef
		want   bool
	}{
		{
			name:   "不存在的实例 id → 剔除",
			access: member,
			ref:    assistantContextEntityRef{Type: "instance", ID: "99999999"},
			want:   false,
		},
		{
			name:   "他人的实例（存在但无权）→ 剔除",
			access: member,
			ref:    assistantContextEntityRef{Type: "instance", ID: assistantContextID("placeholder")}, // 下面用真实 id 覆盖
			want:   false,
		},
		{
			name:   "平台管理员可访问任意实例",
			access: admin,
			ref:    assistantContextEntityRef{Type: "instance", ID: "1"},
			want:   true,
		},
	}
	// 用真实 id 覆盖「他人实例」那条
	cases[1].ref = assistantContextEntityRef{Type: "instance", ID: assistantContextID(strconv.FormatUint(uint64(inst.ID), 10))}

	for _, tc := range cases {
		ok, normalized := svc.contextEntityAccessible(context.Background(), tc.access, tc.ref)
		if ok != tc.want {
			t.Fatalf("%s：可达性应为 %v，得到 %v（normalized=%q）", tc.name, tc.want, ok, normalized)
		}
	}

	// 关键：无论不存在还是无权，返回的都是同一个布尔 false —— 调用方拿不到区分信号。
	// 这条断言锁的是**接口形状**：将来若改成 (bool, error) 并区分错误类型，这里会编译失败。
	okA, _ := svc.contextEntityAccessible(context.Background(), member,
		assistantContextEntityRef{Type: "instance", ID: "99999999"})
	okB, _ := svc.contextEntityAccessible(context.Background(), member,
		assistantContextEntityRef{Type: "instance", ID: assistantContextID(strconv.FormatUint(uint64(inst.ID), 10))})
	if okA != okB || okA != false {
		t.Fatalf("「不存在」与「无权」必须返回同一结果（false），否则上下文成了存在性探测接口：%v vs %v", okA, okB)
	}
}
