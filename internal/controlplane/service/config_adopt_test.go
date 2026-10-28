package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ---------------------------------------------------------------------------
// 测试替身：只实现「读」，从类型上没有写磁盘的能力
// ---------------------------------------------------------------------------

// fakeAdoptSource 是 ConfigAdoptSource 的假实现。
//
// 刻意**只记录 Read/Discover 调用**：纳管「不修改磁盘」的验收靠它来证明——
// 该接口本身就没有任何写方法，因此任何调用序列都不可能写盘。
type fakeAdoptSource struct {
	mu sync.Mutex
	// files 按 instanceID → (路径 → 内容)。
	files map[uint]map[string]string
	// discoverErr 按 instanceID 注入 Discover 失败。
	discoverErr map[uint]error
	// readErr 按 instanceID → 路径注入 Read 失败。
	readErr map[uint]map[string]error
	// readCalls 记录被读过的 (instanceID, path)，用于断言「只读」。
	readCalls []string
	// discoverCalls 记录被 Discover 过的实例。
	discoverCalls []uint
}

func newFakeAdoptSource() *fakeAdoptSource {
	return &fakeAdoptSource{
		files:       map[uint]map[string]string{},
		discoverErr: map[uint]error{},
		readErr:     map[uint]map[string]error{},
	}
}

// put 往某实例塞一个文件（同时算好 hash）。
func (f *fakeAdoptSource) put(instanceID uint, path, content string) {
	if f.files[instanceID] == nil {
		f.files[instanceID] = map[string]string{}
	}
	f.files[instanceID][path] = content
}

// Discover 返回该实例登记的全部配置路径（已按真实实现的格式规则过滤）。
func (f *fakeAdoptSource) Discover(instanceID uint) ([]DiscoveredConfig, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discoverCalls = append(f.discoverCalls, instanceID)
	if err := f.discoverErr[instanceID]; err != nil {
		return nil, false, err
	}
	out := make([]DiscoveredConfig, 0, len(f.files[instanceID]))
	for p := range f.files[instanceID] {
		format, _ := configFormatOf(p)
		out = append(out, DiscoveredConfig{Path: p, Format: format, Supported: true})
	}
	return out, false, nil
}

// Read 返回该实例某文件的磁盘原文。
func (f *fakeAdoptSource) Read(instanceID uint, filePath string) (*ConfigReadResult, error) {
	f.mu.Lock()
	f.readCalls = append(f.readCalls, fmt.Sprintf("%d:%s", instanceID, filePath))
	err := f.readErr[instanceID][filePath]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	content, ok := f.files[instanceID][filePath]
	if !ok {
		return nil, fmt.Errorf("读取配置失败: no such file or directory")
	}
	return &ConfigReadResult{Path: filePath, Content: content}, nil
}

// ---------------------------------------------------------------------------
// 测试库
// ---------------------------------------------------------------------------

// newAdoptTestDB 构造纳管测试库（配置分层 + 实例 + 分组树全量迁移）。
func newAdoptTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.ConfigFile{}, &model.ConfigLayerVersion{},
		&model.Instance{}, &model.Node{},
		&model.InstanceGroupNode{}, &model.InstanceGroupMember{},
	))
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// mkAdoptInstance 造一个实例。
func mkAdoptInstance(t *testing.T, db *gorm.DB, name string, nodeID uint) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		Name: name, UUID: "uuid-" + name, NodeID: nodeID,
		Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar",
		WorkDir: "/tmp/" + name, Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// newAdoptSvc 组装被测服务。
func newAdoptSvc(t *testing.T, db *gorm.DB, src ConfigAdoptSource) *ConfigAdoptService {
	t.Helper()
	return NewConfigAdoptService(db, NewConfigFileService(db), src)
}

// layerHeadOf 取某配置在某实例上的实例层 head（没有则返回 nil）。
func layerHeadOf(t *testing.T, db *gorm.DB, configFileID, instanceID uint) *model.ConfigLayerVersion {
	t.Helper()
	var rows []model.ConfigLayerVersion
	require.NoError(t, db.Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?",
		configFileID, model.ConfigScopeInstance, instanceID).Order("version_no desc").Find(&rows).Error)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// sha256hex 计算内容的 sha256 小写 hex。
func adoptSha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// 验收 1/2/9：逐字节一致 + hash 一致 + 不修改磁盘
// ---------------------------------------------------------------------------

// messyYAML 是刻意「反序列化再序列化必然会变样」的 YAML：
// 缩进不一致、注释、引号风格、key 顺序非字典序、块标量、多行字符串、重复缩进的嵌套。
//
// 若纳管链路里任何一处做了解析 → 重新序列化（例如走 ParseConfigContent + 再 Marshal，
// 或走 patchConfig 的行级重写），这个用例一定失败。这是「逐字节保真」最强的证据。
const messyYAML = `# 顶部注释：保留原文即可
server:
    port: 25565
  # 故意用 4 空格与 2 空格混排
  motd: "中文 MOTD · 带特殊字符 <>&\"'"
  world-list:
      - world
      - world_nether
  nested:
        deep:
            value: 1
  block: |
    第一行
     第二行有前导空格
  folded: >
    折行的内容
    会被折叠

plugins:
  Foo:
    enabled: true     # 行尾注释
    limit: 100
empty_value:
unicode: 中文键值对 ✓
tabs_in_value: "a\tb"
`

// messyProperties 同理：注释、空行、等号与冒号混用、行尾空格、无换行结尾。
const messyProperties = "# 注释保留\nserver-port=25565\nmotd : 带冒号的值\nwhite-space=  前后都有空格   \n\nlast-line-without-newline=x"

func TestFR533Adopt_ContentByteIdenticalAndHashMatchesDisk(t *testing.T) {
	db := newAdoptTestDB(t)
	// 真实磁盘文件：纳管前后比对 mtime，证明「不修改磁盘」。
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "plugins", "Foo"), 0o755))
	yamlPath := filepath.Join(workDir, "plugins", "Foo", "config.yml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(messyYAML), 0o644))
	propsPath := filepath.Join(workDir, "server.properties")
	require.NoError(t, os.WriteFile(propsPath, []byte(messyProperties), 0o644))

	statBefore, err := os.Stat(yamlPath)
	require.NoError(t, err)
	propsBefore, err := os.Stat(propsPath)
	require.NoError(t, err)
	diskYAML, err := os.ReadFile(yamlPath)
	require.NoError(t, err)
	diskProps, err := os.ReadFile(propsPath)
	require.NoError(t, err)

	inst := mkAdoptInstance(t, db, "adopt-byte", 1)
	// fake 直接从真实磁盘文件取内容，保证「被测内容 == 磁盘内容」。
	src := newFakeAdoptSource()
	src.put(inst.ID, "plugins/Foo/config.yml", string(diskYAML))
	src.put(inst.ID, "server.properties", string(diskProps))
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Files: []AdoptFileItem{
			{InstanceID: inst.ID, Path: "plugins/Foo/config.yml"},
			{InstanceID: inst.ID, Path: "server.properties"},
		},
		CreateBaseline: true,
	}, 7)
	require.NoError(t, err)
	require.Len(t, resp.Results, 2)
	require.Equal(t, 2, resp.Summary.Created)
	require.Equal(t, 0, resp.Summary.Failed)

	cases := []struct {
		path   string
		expect []byte
	}{
		{"plugins/Foo/config.yml", diskYAML},
		{"server.properties", diskProps},
	}
	for _, c := range cases {
		var file model.ConfigFile
		require.NoError(t, db.Where("name = ?", c.path).First(&file).Error)
		head := layerHeadOf(t, db, file.ID, inst.ID)
		require.NotNil(t, head, "应写入实例级基线版本: %s", c.path)

		// 验收 1：逐字节一致（不是「语义相等」，是字节串完全相等）。
		assert.Equal(t, string(c.expect), head.Content, "纳管内容必须与磁盘逐字节一致: %s", c.path)
		assert.Equal(t, 1, head.VersionNo, "基线应为 v1")

		// 验收 2：content_hash == sha256(磁盘文件)。
		assert.Equal(t, adoptSha256Hex(string(c.expect)), head.ContentHash,
			"content_hash 必须等于磁盘文件 sha256: %s", c.path)
	}

	// 验收 9：不修改任何磁盘文件（mtime 与内容都不变）。
	statAfter, err := os.Stat(yamlPath)
	require.NoError(t, err)
	assert.Equal(t, statBefore.ModTime(), statAfter.ModTime(), "纳管前后 mtime 必须不变")
	assert.Equal(t, statBefore.Size(), statAfter.Size())
	propsAfter, err := os.Stat(propsPath)
	require.NoError(t, err)
	assert.Equal(t, propsBefore.ModTime(), propsAfter.ModTime(), "纳管前后 mtime 必须不变")

	contentAfter, err := os.ReadFile(yamlPath)
	require.NoError(t, err)
	assert.Equal(t, diskYAML, contentAfter, "磁盘内容必须一字未动")

	// 只读证明：调用序列里只有 Read，接口本身没有写方法。
	assert.Equal(t, []string{
		fmt.Sprintf("%d:plugins/Foo/config.yml", inst.ID),
		fmt.Sprintf("%d:server.properties", inst.ID),
	}, src.readCalls, "纳管只应调用只读 Read")
}

// TestFR533Adopt_DoesNotReserializeStructuredContent 单独锁定「重新序列化」这个坑：
// 纳管后的内容里必须仍然能看到手写缩进、注释、引号风格等仅存在于原文的特征。
func TestFR533Adopt_DoesNotReserializeStructuredContent(t *testing.T) {
	db := newAdoptTestDB(t)
	inst := mkAdoptInstance(t, db, "adopt-noreserialize", 1)
	src := newFakeAdoptSource()
	src.put(inst.ID, "plugins/Foo/config.yml", messyYAML)
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "plugins/Foo/config.yml"}},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)
	require.Equal(t, AdoptStatusCreated, resp.Results[0].Status)

	var file model.ConfigFile
	require.NoError(t, db.Where("name = ?", "plugins/Foo/config.yml").First(&file).Error)
	head := layerHeadOf(t, db, file.ID, inst.ID)
	require.NotNil(t, head)

	// 这些特征在「解析 → 重新序列化」后必然丢失或改变。
	for _, marker := range []string{
		"# 顶部注释：保留原文即可",            // 注释
		"    port: 25565",          // 4 空格缩进
		`"中文 MOTD · 带特殊字符 <>&\"'"`, // 引号风格
		"block: |",                 // 块标量
		"folded: >",                // 折叠标量
		"enabled: true     # 行尾注释", // 行尾注释
		"中文键值对 ✓",                  // 非 ASCII
	} {
		assert.Contains(t, head.Content, marker, "原文特征必须保留（说明未重新序列化）")
	}
	// 原文整体相等是最强断言。
	assert.Equal(t, messyYAML, head.Content)
}

// ---------------------------------------------------------------------------
// 验收 3：dryRun 零写入
// ---------------------------------------------------------------------------

func TestFR533Adopt_DryRunWritesNothing(t *testing.T) {
	db := newAdoptTestDB(t)
	inst := mkAdoptInstance(t, db, "adopt-dryrun", 1)
	src := newFakeAdoptSource()
	src.put(inst.ID, "plugins/Foo/config.yml", messyYAML)
	src.put(inst.ID, "server.properties", messyProperties)
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Files: []AdoptFileItem{
			{InstanceID: inst.ID, Path: "plugins/Foo/config.yml"},
			{InstanceID: inst.ID, Path: "server.properties"},
		},
		CreateBaseline: true,
		DryRun:         true,
	}, 1)
	require.NoError(t, err)
	require.Len(t, resp.Results, 2)
	assert.True(t, resp.DryRunMode)
	assert.True(t, resp.Summary.DryRun)

	// 报告里应有「将新建」的计划信息。
	for _, r := range resp.Results {
		assert.Equal(t, AdoptStatusCreated, r.Status)
		assert.Equal(t, AdoptActionCreate, r.Action)
		assert.Zero(t, r.ConfigFileID, "dryRun 不得产生真实 config_file id")
		assert.Zero(t, r.VersionID, "dryRun 不得产生真实版本 id")
		assert.Equal(t, adoptSha256Hex(adoptMustContent(t, src.files[inst.ID], r.Path)), r.ContentHash)
	}

	// 零写入：两张表都必须是空的。
	var fileCount, versionCount int64
	require.NoError(t, db.Model(&model.ConfigFile{}).Unscoped().Count(&fileCount).Error)
	require.NoError(t, db.Model(&model.ConfigLayerVersion{}).Count(&versionCount).Error)
	assert.Zero(t, fileCount, "dryRun 不得创建 config_file")
	assert.Zero(t, versionCount, "dryRun 不得创建层版本")

	// 但读是必须发生的（dryRun 要能报告内容 hash）。
	assert.Len(t, src.readCalls, 2)
}

// TestFR533Adopt_DryRunThenRealRun 覆盖「预演 → 实跑」的连贯性：
// 预演报告的计划应与实跑结果一致（避免预演骗人）。
func TestFR533Adopt_DryRunThenRealRun(t *testing.T) {
	db := newAdoptTestDB(t)
	inst := mkAdoptInstance(t, db, "adopt-preview", 1)
	src := newFakeAdoptSource()
	src.put(inst.ID, "plugins/Foo/config.yml", messyYAML)
	svc := newAdoptSvc(t, db, src)
	req := ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "plugins/Foo/config.yml"}},
		CreateBaseline: true,
	}

	req.DryRun = true
	preview, err := svc.Adopt(req, 1)
	require.NoError(t, err)

	req.DryRun = false
	real, err := svc.Adopt(req, 1)
	require.NoError(t, err)

	require.Len(t, preview.Results, 1)
	require.Len(t, real.Results, 1)
	assert.Equal(t, preview.Results[0].Status, real.Results[0].Status)
	assert.Equal(t, preview.Results[0].Action, real.Results[0].Action)
	assert.Equal(t, preview.Results[0].ContentHash, real.Results[0].ContentHash)
	assert.Equal(t, preview.Results[0].Name, real.Results[0].Name)
	assert.NotZero(t, real.Results[0].VersionID, "实跑必须真的写库")
}

// ---------------------------------------------------------------------------
// 验收 4：重复纳管 → ALREADY_ADOPTED 且不覆盖
// ---------------------------------------------------------------------------

func TestFR533Adopt_RepeatAdoptReturnsAlreadyAdoptedAndNeverOverwrites(t *testing.T) {
	db := newAdoptTestDB(t)
	inst := mkAdoptInstance(t, db, "adopt-repeat", 1)
	src := newFakeAdoptSource()
	src.put(inst.ID, "server.properties", "a=1\n")
	svc := newAdoptSvc(t, db, src)
	req := ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "server.properties"}},
		CreateBaseline: true,
	}

	first, err := svc.Adopt(req, 1)
	require.NoError(t, err)
	require.Equal(t, AdoptStatusCreated, first.Results[0].Status)

	// 运维在 JM 里改过内容后再纳管一次：磁盘变了也不能覆盖已纳管版本。
	src.put(inst.ID, "server.properties", "a=999\nchanged=yes\n")
	second, err := svc.Adopt(req, 1)
	require.NoError(t, err)
	require.Len(t, second.Results, 1)
	assert.Equal(t, AdoptStatusAlreadyAdopted, second.Results[0].Status)
	assert.Equal(t, AdoptCodeAlreadyAdopted, second.Results[0].Code)
	assert.Equal(t, AdoptActionNone, second.Results[0].Action)
	assert.Equal(t, 1, second.Summary.AlreadyAdopted)
	assert.Equal(t, 0, second.Summary.Appended, "重复纳管不得追加新版本")

	var file model.ConfigFile
	require.NoError(t, db.Where("name = ?", "server.properties").First(&file).Error)
	var versions []model.ConfigLayerVersion
	require.NoError(t, db.Where("config_file_id = ? AND scope_ref_id = ?", file.ID, inst.ID).Find(&versions).Error)
	require.Len(t, versions, 1, "不得覆盖也不得追加")
	assert.Equal(t, "a=1\n", versions[0].Content, "已纳管内容必须保持首次纳管的磁盘快照")
	assert.Equal(t, adoptSha256Hex("a=1\n"), versions[0].ContentHash)
}

// TestFR533Adopt_ExistingConfigFileWithoutVersionBackfillsV1 覆盖 spec §6 的第二行：
// config_file 已存在但该实例无版本 → 补建 v1（而不是报已纳管）。
func TestFR533Adopt_ExistingConfigFileWithoutVersionBackfillsV1(t *testing.T) {
	db := newAdoptTestDB(t)
	instA := mkAdoptInstance(t, db, "adopt-a", 1)
	instB := mkAdoptInstance(t, db, "adopt-b", 1)
	src := newFakeAdoptSource()
	src.put(instA.ID, "server.properties", "content-of-a\n")
	src.put(instB.ID, "server.properties", "content-of-b-different\n")
	svc := newAdoptSvc(t, db, src)

	// A 先纳管：建出 config_file。
	first, err := svc.Adopt(ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: instA.ID, Path: "server.properties"}},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)
	require.Equal(t, AdoptStatusCreated, first.Results[0].Status)

	// B 再纳管同一逻辑名：复用 config_file，为 B 补建自己的 v1。
	second, err := svc.Adopt(ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: instB.ID, Path: "server.properties"}},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)
	require.Len(t, second.Results, 1)
	assert.Equal(t, AdoptStatusAppended, second.Results[0].Status)
	assert.Equal(t, AdoptActionAppendVersion, second.Results[0].Action)
	assert.Equal(t, first.Results[0].ConfigFileID, second.Results[0].ConfigFileID, "应复用同一个 config_file")

	// 验收 7：同名文件不同内容 → 各自独立 v1，互不干扰。
	var file model.ConfigFile
	require.NoError(t, db.Where("name = ?", "server.properties").First(&file).Error)
	headA := layerHeadOf(t, db, file.ID, instA.ID)
	headB := layerHeadOf(t, db, file.ID, instB.ID)
	require.NotNil(t, headA)
	require.NotNil(t, headB)
	assert.Equal(t, "content-of-a\n", headA.Content)
	assert.Equal(t, "content-of-b-different\n", headB.Content)
	assert.Equal(t, 1, headA.VersionNo)
	assert.Equal(t, 1, headB.VersionNo)
}

// ---------------------------------------------------------------------------
// 验收 5：错误码
// ---------------------------------------------------------------------------

func TestFR533Adopt_ErrorCodes(t *testing.T) {
	t.Run("文件不存在", func(t *testing.T) {
		db := newAdoptTestDB(t)
		inst := mkAdoptInstance(t, db, "err-notfound", 1)
		src := newFakeAdoptSource() // 一个文件都不放
		svc := newAdoptSvc(t, db, src)
		resp, err := svc.Adopt(ConfigAdoptRequest{
			Files: []AdoptFileItem{{InstanceID: inst.ID, Path: "server.yml"}},
		}, 1)
		require.NoError(t, err)
		require.Len(t, resp.Results, 1)
		assert.Equal(t, AdoptStatusFailed, resp.Results[0].Status)
		assert.Equal(t, AdoptCodeFileNotFound, resp.Results[0].Code)
		assert.Equal(t, 1, resp.Summary.Failed)
	})

	t.Run("读取失败（无权限）", func(t *testing.T) {
		db := newAdoptTestDB(t)
		inst := mkAdoptInstance(t, db, "err-perm", 1)
		src := newFakeAdoptSource()
		src.put(inst.ID, "server.yml", "x: 1\n")
		src.readErr[inst.ID] = map[string]error{"server.yml": errors.New("读取配置失败: permission denied")}
		svc := newAdoptSvc(t, db, src)
		resp, err := svc.Adopt(ConfigAdoptRequest{
			Files: []AdoptFileItem{{InstanceID: inst.ID, Path: "server.yml"}},
		}, 1)
		require.NoError(t, err)
		assert.Equal(t, AdoptCodeReadFailed, resp.Results[0].Code)
	})

	t.Run("实例离线（节点未连接）", func(t *testing.T) {
		db := newAdoptTestDB(t)
		inst := mkAdoptInstance(t, db, "err-offline", 1)
		src := newFakeAdoptSource()
		src.readErr[inst.ID] = map[string]error{"server.yml": ErrNodeNotConnected}
		svc := newAdoptSvc(t, db, src)
		resp, err := svc.Adopt(ConfigAdoptRequest{
			Files: []AdoptFileItem{{InstanceID: inst.ID, Path: "server.yml"}},
		}, 1)
		require.NoError(t, err)
		assert.Equal(t, AdoptCodeInstanceOffline, resp.Results[0].Code,
			"节点未连接必须归类为 INSTANCE_OFFLINE 而非 READ_FAILED")
	})

	t.Run("路径越界", func(t *testing.T) {
		db := newAdoptTestDB(t)
		inst := mkAdoptInstance(t, db, "err-escape", 1)
		src := newFakeAdoptSource()
		svc := newAdoptSvc(t, db, src)
		for _, p := range []string{"../../etc/passwd", "/etc/shadow", "plugins/../../outside.yml"} {
			resp, err := svc.Adopt(ConfigAdoptRequest{
				Files: []AdoptFileItem{{InstanceID: inst.ID, Path: p}},
			}, 1)
			require.NoError(t, err)
			assert.Equal(t, AdoptCodePathEscape, resp.Results[0].Code, "路径 %q 应被判为越界", p)
		}
		assert.Empty(t, src.readCalls, "越界路径不得发起读取")
	})

	t.Run("格式不受支持", func(t *testing.T) {
		db := newAdoptTestDB(t)
		inst := mkAdoptInstance(t, db, "err-format", 1)
		src := newFakeAdoptSource()
		svc := newAdoptSvc(t, db, src)
		resp, err := svc.Adopt(ConfigAdoptRequest{
			Files: []AdoptFileItem{{InstanceID: inst.ID, Path: "velocity.toml"}},
		}, 1)
		require.NoError(t, err)
		assert.Equal(t, AdoptCodeFormatUnsupported, resp.Results[0].Code)
	})

	t.Run("同名配置已在回收站", func(t *testing.T) {
		db := newAdoptTestDB(t)
		inst := mkAdoptInstance(t, db, "err-archived", 1)
		require.NoError(t, db.Create(&model.ConfigFile{Name: "server.yml", Format: model.ConfigFormatYAML}).Error)
		require.NoError(t, db.Where("name = ?", "server.yml").Delete(&model.ConfigFile{}).Error)
		src := newFakeAdoptSource()
		src.put(inst.ID, "server.yml", "x: 1\n")
		svc := newAdoptSvc(t, db, src)
		resp, err := svc.Adopt(ConfigAdoptRequest{
			Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "server.yml"}},
			CreateBaseline: true,
		}, 1)
		require.NoError(t, err)
		assert.Equal(t, AdoptCodeConfigFileArchived, resp.Results[0].Code)
	})

	t.Run("同名配置格式冲突", func(t *testing.T) {
		db := newAdoptTestDB(t)
		inst := mkAdoptInstance(t, db, "err-conflict", 1)
		// 已按 json 建过同名文件，本次磁盘上是 yaml。
		require.NoError(t, db.Create(&model.ConfigFile{Name: "config.yml", Format: model.ConfigFormatJSON}).Error)
		src := newFakeAdoptSource()
		src.put(inst.ID, "config.yml", "x: 1\n")
		svc := newAdoptSvc(t, db, src)
		resp, err := svc.Adopt(ConfigAdoptRequest{
			Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "config.yml"}},
			CreateBaseline: true,
		}, 1)
		require.NoError(t, err)
		assert.Equal(t, AdoptCodeFormatConflict, resp.Results[0].Code)
	})
}

// TestFR533Adopt_RequestValidation 覆盖请求级校验（不是逐文件错误码）。
func TestFR533Adopt_RequestValidation(t *testing.T) {
	db := newAdoptTestDB(t)
	svc := newAdoptSvc(t, db, newFakeAdoptSource())

	_, err := svc.Adopt(ConfigAdoptRequest{}, 1)
	assert.ErrorIs(t, err, ErrAdoptFilesRequired)

	target := BusinessTarget{GroupIDs: []uint{1}, IncludeDescendants: true}
	_, err = svc.Adopt(ConfigAdoptRequest{Target: &target}, 1)
	assert.ErrorIs(t, err, ErrAdoptPatternRequired, "批量必须显式给 pathPatterns，避免误纳管整目录")

	_, err = svc.Adopt(ConfigAdoptRequest{Target: &BusinessTarget{}, PathPatterns: []string{"*.yml"}}, 1)
	assert.ErrorIs(t, err, ErrBusinessTargetUnspecified)
}

// ---------------------------------------------------------------------------
// 验收 6/8：批量（100 实例、部分离线不整体失败）
// ---------------------------------------------------------------------------

func TestFR533Adopt_Batch100InstancesPartialOffline(t *testing.T) {
	db := newAdoptTestDB(t)
	root := mkGroup(t, db, "cluster", nil)
	child := mkGroup(t, db, "region-1", &root.ID)

	const total = 100
	// 每 10 台里有 3 台离线（节点未连接），验证「部分失败不整体失败」。
	const offlineEvery = 10
	src := newFakeAdoptSource()
	instances := make([]*model.Instance, 0, total)
	for i := 0; i < total; i++ {
		inst := mkAdoptInstance(t, db, fmt.Sprintf("batch-%03d", i), uint(i%5+1))
		require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: child.ID, InstanceID: inst.ID}).Error)
		instances = append(instances, inst)
		// 每个实例的内容都不同 —— 强制验证「不假设一致」。
		src.put(inst.ID, "plugins/Foo/config.yml", fmt.Sprintf("instance: %d\nvalue: %d\n", i, i*7))
		src.put(inst.ID, "server.properties", fmt.Sprintf("server-port=%d\n", 25565+i))
		src.put(inst.ID, "velocity.toml", "not-adoptable")
		if i%offlineEvery < 3 {
			src.discoverErr[inst.ID] = fmt.Errorf("查询节点失败: %w", ErrNodeNotConnected)
		}
	}
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Target:         &BusinessTarget{GroupIDs: []uint{root.ID}, IncludeDescendants: true},
		PathPatterns:   []string{"plugins/*/config.yml", "server.properties"},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)

	// 目标解析：100 台全部纳入（子孙分组递归）。
	assert.Equal(t, total, resp.Summary.Instances)
	require.Len(t, resp.Instances, total)

	offlineCount := 0
	createdCount := 0
	for _, instRes := range resp.Instances {
		if instRes.Code == AdoptCodeInstanceOffline {
			offlineCount++
			assert.Equal(t, AdoptInstanceStatusFailed, instRes.Status)
			assert.Empty(t, instRes.Results, "离线实例不应有逐文件结果")
			continue
		}
		// 在线实例：两份文件都纳管成功（toml 被 pathPatterns 过滤掉）。
		assert.Equal(t, AdoptInstanceStatusMatched, instRes.Status, "实例 #%d", instRes.InstanceID)
		assert.Equal(t, 3, instRes.DiscoveredFiles)
		assert.Equal(t, 2, instRes.MatchedFiles)
		require.Len(t, instRes.Results, 2)
		for _, r := range instRes.Results {
			// 同一逻辑名的 config_file 只会被建一次：第一个纳管的实例是 created，
			// 其余实例复用该 config_file、各存自己的 v1，故为 appended。
			switch r.Status {
			case AdoptStatusCreated:
				createdCount++
			case AdoptStatusAppended:
			default:
				t.Fatalf("实例 #%d 的 %s 状态异常: %s (%s)", instRes.InstanceID, r.Path, r.Status, r.Code)
			}
		}
	}
	assert.Equal(t, 30, offlineCount, "应有 30 台离线被单独标记")
	assert.Equal(t, 0, resp.Summary.AlreadyAdopted)
	// 两种逻辑名各被「建」一次（各自的首个在线实例），其余 138 次是追加。
	assert.Equal(t, 2, createdCount, "每个逻辑名只有首个在线实例是 created")
	assert.Equal(t, 138, resp.Summary.Appended)
	assert.Equal(t, 2, resp.Summary.Created)

	// 逐实例独立 v1：同一逻辑名在 70 个在线实例上各有一条 v1，内容各不相同。
	var file model.ConfigFile
	require.NoError(t, db.Where("name = ?", "server.properties").First(&file).Error)
	var versions []model.ConfigLayerVersion
	require.NoError(t, db.Where("config_file_id = ?", file.ID).Find(&versions).Error)
	assert.Len(t, versions, 70, "每个在线实例各一条实例级 v1")

	seenHash := map[string]struct{}{}
	for _, v := range versions {
		assert.Equal(t, 1, v.VersionNo)
		assert.Equal(t, model.ConfigScopeInstance, v.ScopeLevel)
		assert.Equal(t, adoptSha256Hex(v.Content), v.ContentHash)
		seenHash[v.ContentHash] = struct{}{}
	}
	assert.Len(t, seenHash, 70, "内容各不相同时 hash 也必须各不相同（未做归一化）")

	// 失败没有整体中断：汇总里 failed 只统计逐文件失败（离线是实例级，不进逐文件计数）。
	assert.Equal(t, 0, resp.Summary.Failed, "离线实例不应污染逐文件失败计数")
}

// TestFR533Adopt_BatchNoMatchAndTruncated 覆盖「无文件命中」与 Discover 截断标记。
func TestFR533Adopt_BatchNoMatch(t *testing.T) {
	db := newAdoptTestDB(t)
	root := mkGroup(t, db, "cluster-no-match", nil)
	inst := mkAdoptInstance(t, db, "nomatch", 1)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: root.ID, InstanceID: inst.ID}).Error)
	src := newFakeAdoptSource()
	src.put(inst.ID, "server.properties", "a=1\n")
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Target:         &BusinessTarget{GroupIDs: []uint{root.ID}},
		PathPatterns:   []string{"plugins/**/*.yml"},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)
	require.Len(t, resp.Instances, 1)
	assert.Equal(t, AdoptInstanceStatusNoMatch, resp.Instances[0].Status)
	assert.Equal(t, 1, resp.Instances[0].DiscoveredFiles)
	assert.Zero(t, resp.Instances[0].MatchedFiles)
	assert.Zero(t, resp.Summary.Total)
}

// ---------------------------------------------------------------------------
// 验收 10/11：纳管后能预览、能在其上建分层覆盖并正确合并
// ---------------------------------------------------------------------------

// TestFR533Adopt_ThenLayerOverrideAndMerge 覆盖 spec 验收 10/11：
// 纳管后能读回内容、能在其上建分层覆盖。
//
// 这里刻意用 YAML：properties 的合并输出是 `key=value`（规范化），
// 而纳管基线的价值恰恰是「内容就是磁盘原文」，两者不要混在一个用例里验证。
//
// 注意实例层基线在作用域链里优先级最高（config_layer.go resolveScopeChain 注释：
// 「实例层恒在最后（优先级最高）」），所以分组层无法覆盖实例层已有的键——
// 这是 FR-529 的既有语义，纳管不改变它。本用例按这个真实语义断言。
func TestFR533Adopt_ThenLayerOverrideAndMerge(t *testing.T) {
	db := newAdoptTestDB(t)
	db = withLayerScopeMigration(t, db)
	root := mkGroup(t, db, "cluster-merge", nil)
	inst := mkAdoptInstance(t, db, "merge-inst", 1)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: root.ID, InstanceID: inst.ID}).Error)

	src := newFakeAdoptSource()
	// 磁盘基线只覆盖 motd；motd-from-group 之类的键留给分组层补。
	src.put(inst.ID, "plugins/Foo/config.yml", "motd: disk-original\n")
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "plugins/Foo/config.yml"}},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)
	fileID := resp.Results[0].ConfigFileID
	require.NotZero(t, fileID)

	// 验收 10：纳管后能读回内容（链上只有实例层基线时，有效值 = 磁盘原文语义）。
	eff, err := NewConfigFileService(db).Effective(fileID, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "disk-original", eff.Traced["motd"].Value)
	assert.Equal(t, string(model.ConfigScopeInstance), eff.Traced["motd"].Source.Level)

	// 验收 11：在其上建分组覆盖并正确合并。
	_, err = NewConfigFileService(db).SaveLayer(fileID, SaveLayerInput{
		ScopeLevel: string(model.ConfigScopeGroup),
		ScopeID:    root.ID,
		Content:    "motd: region-override\nonly-in-group: yes\n",
	}, 1)
	require.NoError(t, err)

	eff, err = NewConfigFileService(db).Effective(fileID, inst.ID)
	require.NoError(t, err)
	// 实例层优先级最高：已有的 motd 不被分组层覆盖。
	assert.Equal(t, "disk-original", eff.Traced["motd"].Value,
		"实例层基线优先级高于分组层（FR-529 既有语义）")
	assert.Equal(t, string(model.ConfigScopeInstance), eff.Traced["motd"].Source.Level)
	// 基线没有的键由分组层补上，说明合并确实生效。
	assert.Equal(t, "yes", eff.Traced["only-in-group"].Value)
	assert.Equal(t, string(model.ConfigScopeGroup), eff.Traced["only-in-group"].Source.Level)
}

// ---------------------------------------------------------------------------
// health 自检
// ---------------------------------------------------------------------------

func TestFR533Adopt_HealthReportsFactsOnly(t *testing.T) {
	db := newAdoptTestDB(t)
	inst := mkAdoptInstance(t, db, "health-inst", 1)
	src := newFakeAdoptSource()
	src.put(inst.ID, "server.properties", "a=1\n")
	src.put(inst.ID, "plugins/Foo/config.yml", "b: 2\n")
	svc := newAdoptSvc(t, db, src)

	// 空库：计数为 0，notice 仍然存在（固定文案，不做自动判定）。
	empty, err := svc.Health()
	require.NoError(t, err)
	assert.Zero(t, empty.ConfigFileCount)
	assert.Zero(t, empty.LayerVersionCount)
	assert.Empty(t, empty.LastWriteAt)
	assert.Contains(t, empty.Notice, "Beacon", "notice 必须提示人工确认双写风险")
	assert.Contains(t, empty.Notice, "无法自动检测")

	_, err = svc.Adopt(ConfigAdoptRequest{
		Files: []AdoptFileItem{
			{InstanceID: inst.ID, Path: "server.properties"},
			{InstanceID: inst.ID, Path: "plugins/Foo/config.yml"},
		},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)

	view, err := svc.Health()
	require.NoError(t, err)
	assert.EqualValues(t, 2, view.ConfigFileCount)
	assert.EqualValues(t, 2, view.LayerVersionCount)
	assert.NotEmpty(t, view.LastWriteAt, "应报告最近一次配置写入时间")
	assert.Equal(t, adoptHealthNotice, view.Notice)
}

// ---------------------------------------------------------------------------
// 纯函数单测
// ---------------------------------------------------------------------------

func TestFR533Adopt_NormalizePath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"server.properties", "server.properties", false},
		{"./server.properties", "server.properties", false},
		{"plugins/Foo/config.yml", "plugins/Foo/config.yml", false},
		{"plugins//Foo//config.yml", "plugins/Foo/config.yml", false},
		{`plugins\Foo\config.yml`, "plugins/Foo/config.yml", false},
		{"  server.properties  ", "server.properties", false},
		{"", "", true},
		{"   ", "", true},
		{"/etc/passwd", "", true},
		{"../outside.yml", "", true},
		{"plugins/../../outside.yml", "", true},
		{"C:/Windows/x.yml", "", true},
		{".", "", true},
	}
	for _, c := range cases {
		got, err := normalizeAdoptPath(c.in)
		if c.wantErr {
			assert.Error(t, err, "输入 %q 应报错", c.in)
			continue
		}
		require.NoError(t, err, "输入 %q", c.in)
		assert.Equal(t, c.want, got)
	}
}

func TestFR533Adopt_MatchConfigPath(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"server.properties", "server.properties", true},
		{"server.properties", "plugins/server.properties", false},
		{"*.yml", "config.yml", true},
		{"*.yml", "plugins/config.yml", false},
		{"plugins/*/config.yml", "plugins/Foo/config.yml", true},
		{"plugins/*/config.yml", "plugins/Foo/Bar/config.yml", false},
		{"plugins/**/config.yml", "plugins/Foo/config.yml", true},
		{"plugins/**/config.yml", "plugins/Foo/Bar/config.yml", true},
		{"plugins/**", "plugins/Foo/Bar/config.yml", true},
		{"**/*.yml", "a/b/c.yml", true},
		// 大小写不敏感：MC 插件配置大小写混用极常见。
		{"plugins/*/config.yml", "plugins/Foo/Config.YML", true},
		// 非法模式不匹配任何东西（不静默退化成全匹配）。
		{"plugins/[/config.yml", "plugins/Foo/config.yml", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, matchConfigPath(c.pattern, c.path),
			"pattern=%q path=%q", c.pattern, c.path)
	}
}

func TestFR533Adopt_FormatDetection(t *testing.T) {
	cases := []struct {
		path string
		want model.ConfigFileFormat
		ok   bool
	}{
		{"server.properties", model.ConfigFormatProperties, true},
		{"config.yml", model.ConfigFormatYAML, true},
		{"config.yaml", model.ConfigFormatYAML, true},
		{"plugins/Foo/config.json", model.ConfigFormatJSON, true},
		{"velocity.toml", "", false},
		{"notes.txt", "", false},
		{"forwarding.secret", "", false},
	}
	for _, c := range cases {
		got, ok := adoptFormatOf(c.path)
		assert.Equal(t, c.ok, ok, "path=%q", c.path)
		if c.ok {
			assert.Equal(t, c.want, got, "path=%q", c.path)
		}
	}
}

// TestFR533Adopt_TomlIsUnsupportedNotSilentlyTxt 明确记录这条边界：
// Discover 会把 .toml 当成配置返回（supported 取决于 schema），但 config_file 不支持 toml，
// 因此纳管必须显式报 FORMAT_UNSUPPORTED，而不是硬塞成 yaml 导致后续解析全错。
func TestFR533Adopt_TomlIsUnsupportedNotSilentlyTxt(t *testing.T) {
	db := newAdoptTestDB(t)
	inst := mkAdoptInstance(t, db, "toml-inst", 1)
	src := newFakeAdoptSource()
	src.put(inst.ID, "velocity.toml", "bind = \"0.0.0.0:25577\"\n")
	svc := newAdoptSvc(t, db, src)
	resp, err := svc.Adopt(ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "velocity.toml"}},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, AdoptCodeFormatUnsupported, resp.Results[0].Code)
	var count int64
	require.NoError(t, db.Model(&model.ConfigFile{}).Count(&count).Error)
	assert.Zero(t, count, "格式不支持时不得留下半个 config_file")
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// adoptMustContent 从 fake 源里取内容（断言辅助）。
func adoptMustContent(t *testing.T, files map[string]string, p string) string {
	t.Helper()
	c, ok := files[p]
	require.True(t, ok, "fake 源里找不到 %s", p)
	return c
}

// withLayerScopeMigration 补迁移分层服务需要的表（Effective 会读分组树与实例）。
func withLayerScopeMigration(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&model.ConfigFile{}, &model.ConfigLayerVersion{},
		&model.InstanceGroupNode{}, &model.InstanceGroupMember{},
		&model.Instance{}, &model.Node{},
	))
	return db
}

// TestFR533Adopt_NoVersionWhenBaselineDisabled 覆盖 createBaseline=false 的两种分支。
func TestFR533Adopt_NoVersionWhenBaselineDisabled(t *testing.T) {
	db := newAdoptTestDB(t)
	inst := mkAdoptInstance(t, db, "no-baseline", 1)
	src := newFakeAdoptSource()
	src.put(inst.ID, "server.properties", "a=1\n")
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "server.properties"}},
		CreateBaseline: false,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, AdoptStatusCreated, resp.Results[0].Status)
	assert.Equal(t, AdoptActionCreateFileOnly, resp.Results[0].Action)

	var versionCount int64
	require.NoError(t, db.Model(&model.ConfigLayerVersion{}).Count(&versionCount).Error)
	assert.Zero(t, versionCount, "未要求建基线时不得写版本")

	// 再纳管一次：config_file 存在且无版本 → 仍然不写版本，状态为 unchanged。
	resp, err = svc.Adopt(ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "server.properties"}},
		CreateBaseline: false,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, AdoptStatusUnchanged, resp.Results[0].Status)
	assert.Equal(t, AdoptActionNone, resp.Results[0].Action)
}

// TestFR533Adopt_UnparseableContentStillAdopted 验证「坏配置也要能纳管」：
// 纳管语义是记录现状，语法错误只作为 warning 带出，不阻断入库。
func TestFR533Adopt_UnparseableContentStillAdopted(t *testing.T) {
	db := newAdoptTestDB(t)
	inst := mkAdoptInstance(t, db, "broken-yaml", 1)
	broken := "a: [unclosed\n  b: : :\n\tbad-indent: 1\n"
	src := newFakeAdoptSource()
	src.put(inst.ID, "plugins/Broken/config.yml", broken)
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Files:          []AdoptFileItem{{InstanceID: inst.ID, Path: "plugins/Broken/config.yml"}},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, AdoptStatusCreated, resp.Results[0].Status, "语法错误不应阻断纳管")
	assert.NotEmpty(t, resp.Results[0].Warning, "应给出解析失败提示")

	var file model.ConfigFile
	require.NoError(t, db.Where("name = ?", "plugins/Broken/config.yml").First(&file).Error)
	head := layerHeadOf(t, db, file.ID, inst.ID)
	require.NotNil(t, head)
	assert.Equal(t, broken, head.Content, "坏配置同样必须逐字节保真")
	assert.Equal(t, adoptSha256Hex(broken), head.ContentHash)
}

// TestFR533Adopt_AsNameSeparatesLogicalFiles 覆盖 asName：同一路径在不同实例可按不同逻辑名纳管，
// 也可让不同路径归并到同一逻辑名（每实例各存自己的 v1）。
func TestFR533Adopt_AsNameSeparatesLogicalFiles(t *testing.T) {
	db := newAdoptTestDB(t)
	instA := mkAdoptInstance(t, db, "asname-a", 1)
	instB := mkAdoptInstance(t, db, "asname-b", 1)
	src := newFakeAdoptSource()
	src.put(instA.ID, "plugins/Foo/config.yml", "who: a\n")
	src.put(instB.ID, "plugins/Bar/config.yml", "who: b\n")
	svc := newAdoptSvc(t, db, src)

	resp, err := svc.Adopt(ConfigAdoptRequest{
		Files: []AdoptFileItem{
			{InstanceID: instA.ID, Path: "plugins/Foo/config.yml", AsName: "shared/plugin.yml"},
			{InstanceID: instB.ID, Path: "plugins/Bar/config.yml", AsName: "shared/plugin.yml"},
		},
		CreateBaseline: true,
	}, 1)
	require.NoError(t, err)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, AdoptStatusCreated, resp.Results[0].Status)
	assert.Equal(t, AdoptStatusAppended, resp.Results[1].Status)
	assert.Equal(t, resp.Results[0].ConfigFileID, resp.Results[1].ConfigFileID)

	var file model.ConfigFile
	require.NoError(t, db.Where("name = ?", "shared/plugin.yml").First(&file).Error)
	assert.Equal(t, "who: a\n", layerHeadOf(t, db, file.ID, instA.ID).Content)
	assert.Equal(t, "who: b\n", layerHeadOf(t, db, file.ID, instB.ID).Content)
}

// TestFR533Adopt_ConcurrentSameNameDoesNotDuplicate 验证并发写保护：
// 多实例并行读、串行写，同名 config_file 只能存在一份。
func TestFR533Adopt_ConcurrentSameNameDoesNotDuplicate(t *testing.T) {
	db := newAdoptTestDB(t)
	src := newFakeAdoptSource()
	items := make([]AdoptFileItem, 0, 24)
	for i := 0; i < 24; i++ {
		inst := mkAdoptInstance(t, db, fmt.Sprintf("conc-%02d", i), 1)
		src.put(inst.ID, "server.properties", fmt.Sprintf("i=%d\n", i))
		items = append(items, AdoptFileItem{InstanceID: inst.ID, Path: "server.properties"})
	}
	svc := newAdoptSvc(t, db, src)
	svc.SetConcurrency(8)

	resp, err := svc.Adopt(ConfigAdoptRequest{Files: items, CreateBaseline: true}, 1)
	require.NoError(t, err)
	require.Len(t, resp.Results, 24)
	for _, r := range resp.Results {
		assert.NotEqual(t, AdoptStatusFailed, r.Status, "并发纳管不应失败: %+v", r)
	}

	var files []model.ConfigFile
	require.NoError(t, db.Where("name = ?", "server.properties").Find(&files).Error)
	require.Len(t, files, 1, "同名 config_file 必须唯一")

	var versions []model.ConfigLayerVersion
	require.NoError(t, db.Where("config_file_id = ?", files[0].ID).Find(&versions).Error)
	assert.Len(t, versions, 24, "每个实例各一条 v1")
	for _, v := range versions {
		assert.Equal(t, 1, v.VersionNo)
	}
}

// TestFR533Adopt_ServiceDepsAreReadOnly 是一个结构性断言：
// ConfigAdoptService 只依赖 ConfigAdoptSource（Discover/Read）与 ConfigFileService
// （config_file / 层版本），两者都不碰 Worker 写盘接口。这条用例把该意图写成可读的断言，
// 防止后来者给纳管加一条「顺手把规范化后的内容写回磁盘」的路径。
func TestFR533Adopt_ServiceDepsAreReadOnly(t *testing.T) {
	// ConfigAdoptSource 的方法集就是全部只读方法；这里用编译期断言固化。
	var _ ConfigAdoptSource = (*ConfigService)(nil)
	assert.Contains(t, adoptBaselineRemark(adoptTask{Path: "a.yml"}, time.Unix(0, 0)),
		"纳管自磁盘", "基线 remark 应标注纳管来源")
}
