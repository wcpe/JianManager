package vlsup

import "testing"

// TestSupervisorRetentionPerNamespace 钉住「VL retention 可按 namespace 分开」。
//
// 为什么必须有这条：VL 的 retention 到期是**直接删**，它绕过平台侧「删前归档」的硬规则——
// 若热层实例的 retention 短于保留策略的热层窗口，搬运驱动器还没搬，VL 自己先把数据删了。
// 因此热/冷两层必须能各配各的。
func TestSupervisorRetentionPerNamespace(t *testing.T) {
	s, err := New(Options{
		BinaryPath:      "/nonexistent/victoria-logs",
		AssetSHA256:     "deadbeef",
		DataRoot:        t.TempDir(),
		AuthUsername:    "jm",
		AuthPassword:    "secret",
		RetentionPeriod: "90d",
		RetentionByNamespace: map[Namespace]string{
			NamespaceCold: "730d",
		},
		Factory: &fakeFactory{},
	})
	if err != nil {
		t.Fatalf("构造 supervisor 失败：%v", err)
	}
	cases := []struct {
		ns   Namespace
		want string
	}{
		{NamespaceHot, "90d"},
		{NamespaceCold, "730d"},
		{NamespaceRehydrate, "90d"}, // 未单独指定 ⇒ 用回退值
	}
	for _, c := range cases {
		cfg, err := s.Config(c.ns)
		if err != nil {
			t.Fatalf("取 %s 配置失败：%v", c.ns, err)
		}
		if cfg.RetentionPeriod != c.want {
			t.Errorf("%s 的 retention 应为 %q，得到 %q", c.ns, c.want, cfg.RetentionPeriod)
		}
	}

	// 空白值不得被当成有效覆盖：否则「配了个空串」会静默变成「retention 为空」而被 VL 拒。
	s2, err := New(Options{
		BinaryPath:           "/nonexistent/victoria-logs",
		AssetSHA256:          "deadbeef",
		DataRoot:             t.TempDir(),
		AuthUsername:         "jm",
		AuthPassword:         "secret",
		RetentionPeriod:      "90d",
		RetentionByNamespace: map[Namespace]string{NamespaceCold: "   "},
		Factory:              &fakeFactory{},
	})
	if err != nil {
		t.Fatalf("构造 supervisor 失败：%v", err)
	}
	if cfg, _ := s2.Config(NamespaceCold); cfg.RetentionPeriod != "90d" {
		t.Errorf("空白覆盖应回退到回退值 90d，得到 %q", cfg.RetentionPeriod)
	}
}
