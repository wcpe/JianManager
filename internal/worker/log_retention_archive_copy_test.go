package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件守 FR-501「导出归档」的**配置面**：开关默认关、缺目的地启动即拒、
// 三闸里第三闸（VL 侧）读的是 `log_vl.delete_enabled`。
//
// 为什么这些都要走完整链路（YAML → viper → RetentionPolicy）：
// 本包的配置陷阱此前踩过不止一次（`trigger.interval` 被静默丢弃、
// `90d` 让整个 Load 失败）。「键写在 YAML 里但没生效」在现场没有报错，
// 只表现为「功能没按预期跑」——正好是最难查的一类。

// TestRetentionArchiveCopyDefaultsOff 钉住「默认部署零行为变化」。
//
// 这条不是客套：archive_copy 会把到期数据写到另一个目的地，
// 若默认打开，升级动作本身就等于替运维做了「开始导出」的决定。
func TestRetentionArchiveCopyDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	policy, err := cfg.RetentionPolicy()
	if err != nil {
		t.Fatalf("RetentionPolicy 失败：%v", err)
	}
	if policy.ArchiveCopy.Enabled {
		t.Fatal("archive_copy 必须默认关闭")
	}
	// 未显式给根目录时也不得凭空造一个默认值：归档写在哪块盘是运维的决定。
	if got := strings.TrimSpace(cfg.LogRetention.ArchiveCopy.Root); got != "" {
		t.Fatalf("archive_copy.root 不得有默认值，得到 %q", got)
	}
	// 三闸默认必须全关（用户护栏）。
	if open, reason := policy.DeleteGatesOpen(); open {
		t.Fatalf("默认策略下删除三闸必须全关，得到 %q", reason)
	}
}

// TestRetentionArchiveCopyEnabledWithoutRootRejected 钉住「开了开关但没处写」必须启动即拒。
//
// 若静默按未启用处理，运维看到的是 `enabled: true` 而实际什么都没发生，
// 到期数据继续占着热层直到盘满——正是本功能要避免的结局。
func TestRetentionArchiveCopyEnabledWithoutRootRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		// 在 Load 阶段就拒也满足要求（越早越好）。
		if !strings.Contains(err.Error(), "archive_copy.root") {
			t.Fatalf("拒绝原因应点名 archive_copy.root，得到 %v", err)
		}
		return
	}
	if _, err := cfg.RetentionPolicy(); err == nil {
		t.Fatal("启用导出归档但未配置 root 必须报错（不得静默降级为空转）")
	} else if !strings.Contains(err.Error(), "archive_copy.root") {
		t.Fatalf("拒绝原因应点名 archive_copy.root，得到 %v", err)
	}
}

// TestRetentionArchiveCopyAndVLDeleteGateTakesEffect 钉住三个键真的通到策略层：
// `archive_copy.enabled` / `archive_copy.root` / `log_vl.delete_enabled`。
//
// 第三闸尤其重要：它是「受管 VL 真的带 -delete.enable」这个**事实**在策略层的代表，
// 若它恒为 false，回收热层就永远不可达（fail-safe，但也就等于功能没做完）；
// 若它恒为 true，三闸就退化成两闸（危险方向）。两个方向都由本用例钉住。
func TestRetentionArchiveCopyAndVLDeleteGateTakesEffect(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deleteOn bool
	}{
		{"VL 删除关闭（默认）", false},
		{"VL 删除打开", true},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "worker.yml")
		// 夹具里已经有 log_vl: 段，重复写一个同名顶层键会让 YAML 直接拒解析，
		// 故用替换把键插进既有段内（这也是最贴近运维真实改法的形态）。
		base := wiringManagedReceiverYAML
		if tc.deleteOn {
			base = strings.Replace(base, "  start_cold: true", "  start_cold: true\n  delete_enabled: true", 1)
		}
		yaml := base + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
    root: /var/lib/jianmanager/log-archive
`
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatalf("%s：写夹具失败：%v", tc.name, err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("%s：Load 失败：%v", tc.name, err)
		}
		policy, err := cfg.RetentionPolicy()
		if err != nil {
			t.Fatalf("%s：RetentionPolicy 失败：%v", tc.name, err)
		}
		if !policy.ArchiveCopy.Enabled {
			t.Fatalf("%s：archive_copy.enabled 未传到策略层", tc.name)
		}
		if got := cfg.LogRetention.ArchiveCopy.Root; got != "/var/lib/jianmanager/log-archive" {
			t.Fatalf("%s：archive_copy.root 未解析，得到 %q", tc.name, got)
		}
		if policy.Sweep.VLDeleteEnabled != tc.deleteOn {
			t.Fatalf("%s：第三闸应与 log_vl.delete_enabled 一致，得到 %v",
				tc.name, policy.Sweep.VLDeleteEnabled)
		}
	}
}

// TestRetentionArchiveCopyRootFromConfig 保证 root 只被 TrimSpace 影响，不被改写。
func TestRetentionArchiveCopyRootFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
    root: "/data/log-archive"
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	if _, err := cfg.RetentionPolicy(); err != nil {
		t.Fatalf("RetentionPolicy 失败：%v", err)
	}
	if got := cfg.LogRetention.ArchiveCopy.Root; got != "/data/log-archive" {
		t.Fatalf("root 应原样保留，得到 %q", got)
	}
}

// ── FR-501 收尾：导出归档目的地的 s3 / minio 分支 ──
//
// 本组用例只守「配置面能表达、启动期能拒错」这两件事，不碰装配层的真实网络。
// 真实 S3/MinIO 的 SigV4 往返已由 FR-478 的 `storage/s3_realminio_test.go`（env 门控）
// 与 `storage_test.go` 的假服务器签名往返覆盖；这里再搭一套假 S3 只会把
// 「配置是否正确传递」与「S3 协议是否正确实现」两件事混进一条用例。

// TestRetentionArchiveCopyS3ProviderRequiresEndpointAndBucket 钉住「s3/minio 开了但没给端点/桶」启动即拒。
//
// 为什么这两条必须逐项判：少判 endpoint 会在第一次导出时以「对象键拼不出来」的形式失败，
// 而那一刻**热层已经冻结**，退出路径还要解冻；少判 bucket 则会在 SigV4 签名阶段失败，
// 报错文本是 S3 侧的 NoSuchBucket，现场很难联想到是 yaml 少写一行。
func TestRetentionArchiveCopyS3ProviderRequiresEndpointAndBucket(t *testing.T) {
	for _, tc := range []struct {
		name      string
		yamlKeys  string
		wantError string
	}{
		{
			name:      "s3 缺 endpoint",
			yamlKeys:  "    provider: s3\n    bucket: jm-archive\n",
			wantError: "log_retention.archive_copy.endpoint 不能为空",
		},
		{
			name:      "s3 缺 bucket",
			yamlKeys:  "    provider: s3\n    endpoint: minio.example.com:9000\n",
			wantError: "log_retention.archive_copy.bucket 不能为空",
		},
		{
			name:      "minio 缺 bucket",
			yamlKeys:  "    provider: minio\n    endpoint: minio.example.com:9000\n",
			wantError: "log_retention.archive_copy.bucket 不能为空",
		},
		{
			name:      "未知 provider",
			yamlKeys:  "    provider: gcs\n    bucket: jm-archive\n    endpoint: gcs.example.com\n",
			wantError: "log_retention.archive_copy.provider 不支持",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "worker.yml")
			yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
` + tc.yamlKeys
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatalf("写夹具失败：%v", err)
			}
			cfg, err := Load(path)
			if err == nil {
				_, err = cfg.RetentionPolicy()
			}
			// 拒在 Load 阶段也满足要求（越早越好）；本用例只关心「必需被拒」与「拒绝原因点名了哪个键」。
			if err == nil {
				t.Fatalf("期望启动即拒（%s），但配置加载通过了", tc.wantError)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("错误信息应包含 %q，得到 %q", tc.wantError, err.Error())
			}
		})
	}
}

// TestRetentionArchiveCopyS3ProviderParsedFromYAML 钉住 s3 分支的每个键真被解析到配置结构。
//
// 「键写在 YAML 里但没生效」是本包的经典陷阱（`trigger.interval` 曾被静默丢弃）。
// 本用例逐字段断言，任何一个键写错名字都会在这里转红。
func TestRetentionArchiveCopyS3ProviderParsedFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
    provider: s3
    endpoint: minio.example.com:9000
    bucket: jm-archive
    region: cn-east-1
    prefix: logs/retention
    access_key: AKIA-FIXTURE
    secret_key: SECRET-FIXTURE
    use_ssl: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	if _, err := cfg.RetentionPolicy(); err != nil {
		t.Fatalf("RetentionPolicy 失败：%v", err)
	}
	got := cfg.LogRetention.ArchiveCopy
	if got.ArchiveProviderKind() != "s3" {
		t.Fatalf("provider 未解析，得到 %q", got.ArchiveProviderKind())
	}
	if got.Endpoint != "minio.example.com:9000" {
		t.Fatalf("endpoint 未解析，得到 %q", got.Endpoint)
	}
	if got.Bucket != "jm-archive" {
		t.Fatalf("bucket 未解析，得到 %q", got.Bucket)
	}
	if got.Region != "cn-east-1" {
		t.Fatalf("region 未解析，得到 %q", got.Region)
	}
	if got.Prefix != "logs/retention" {
		t.Fatalf("prefix 未解析，得到 %q", got.Prefix)
	}
	if got.AccessKey != "AKIA-FIXTURE" {
		t.Fatalf("access_key 未解析，得到 %q", got.AccessKey)
	}
	if got.SecretKey != "SECRET-FIXTURE" {
		t.Fatal("secret_key 未解析")
	}
	if !got.UseSSL {
		t.Fatal("use_ssl 未解析")
	}
}

// TestRetentionArchiveCopyProviderDefaultsToLocal 钉住「不写 provider 就是 local」。
//
// 这个默认值不能改：把默认值定成 s3 会让「本地目录已够用」的存量部署在升级后
// 凭空多出一套凭证要求（缺 endpoint 即启动即拒）——默认值改变行为是最典型的事故。
func TestRetentionArchiveCopyProviderDefaultsToLocal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
    root: /data/log-archive
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	if _, err := cfg.RetentionPolicy(); err != nil {
		t.Fatalf("RetentionPolicy 失败：%v", err)
	}
	if got := cfg.LogRetention.ArchiveCopy.ArchiveProviderKind(); got != "local" {
		t.Fatalf("provider 默认应为 local，得到 %q", got)
	}
}

// TestRetentionArchiveCopyValidateDestinationIsPure 钉住校验方法本身无副作用、且对空开关宽容。
//
// 这条是防御性的：`RetentionPolicy` 只在 enabled=true 时调用它，而装配层可能直接用它做
// 第二次检查。若它对「未被启用」的形态也报错，就会出现「关了开关反而启动失败」的荒唐形态。
func TestRetentionArchiveCopyValidateDestinationIsPure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     LogRetentionArchiveCopyConfig
		wantErr bool
	}{
		{"local + root", LogRetentionArchiveCopyConfig{Provider: "local", Root: "/x"}, false},
		{"local 无 root", LogRetentionArchiveCopyConfig{Provider: "local"}, true},
		{"空 provider 视同 local + root", LogRetentionArchiveCopyConfig{Root: "/x"}, false},
		{"空 provider 无 root", LogRetentionArchiveCopyConfig{}, true},
		{"s3 齐备", LogRetentionArchiveCopyConfig{Provider: "s3", Endpoint: "e", Bucket: "b"}, false},
		{"minio 齐备", LogRetentionArchiveCopyConfig{Provider: "minio", Endpoint: "e", Bucket: "b"}, false},
		{"大小写与空白归一", LogRetentionArchiveCopyConfig{Provider: "  S3 ", Endpoint: "e", Bucket: "b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.ValidateArchiveCopyDestination()
			if tc.wantErr && err == nil {
				t.Fatal("期望校验失败，但通过了")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望校验通过，得到 %v", err)
			}
		})
	}
}
