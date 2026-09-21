package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service/schema"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func newConfigSourceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Node{}, &model.InstanceConfigSource{},
	))
	return db
}

func seedConfigSourceInstance(t *testing.T, db *gorm.DB, name, startCmd string) *model.Instance {
	t.Helper()
	node := &model.Node{
		Name: "n-" + name, Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101,
		Secret: "s", Status: model.NodeStatusOnline,
	}
	require.NoError(t, db.Create(node).Error)
	in := &model.Instance{
		NodeID: node.ID, Name: name, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, StartCommand: startCmd, WorkDir: "/tmp/" + name,
	}
	require.NoError(t, db.Create(in).Error)
	return in
}

func indexSurface(items []SurfaceItem) map[string]SurfaceItem {
	out := make(map[string]SurfaceItem, len(items))
	for _, it := range items {
		out[it.ItemKey] = it
	}
	return out
}

func mergedField(cfg schema.ParsedConfig, key string) string {
	for _, f := range cfg.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

func TestConfigSource_SurfaceDefaultsForLegacyInstance(t *testing.T) {
	db := newConfigSourceTestDB(t)
	inst := seedConfigSourceInstance(t, db, "legacy", "/usr/bin/java -jar server.jar")
	svc := NewConfigSourceService(db, nil, nil)

	items, err := svc.Surface(inst.ID)
	require.NoError(t, err)
	byKey := indexSurface(items)

	cmd := byKey[ConfigItemStartupCommand]
	require.Equal(t, model.ConfigSourceInline, cmd.Source)
	require.False(t, cmd.Registered)
	require.Equal(t, "/usr/bin/java -jar server.jar", cmd.EffectiveValue)
	require.True(t, cmd.Editable)

	// 未登记的 props 项视作文件隐含、只读（无 Worker，预览降级为 previewError，不阻断）。
	qp := byKey[ConfigItemPropsPrefix+"query.port"]
	require.Equal(t, model.ConfigSourceFile, qp.Source)
	require.False(t, qp.Editable)
	require.Equal(t, defaultServerPropertiesPath, qp.FilePath)
	require.Equal(t, "query.port", qp.FileKey)
}

func TestConfigSource_InlineStartupCommandPersists(t *testing.T) {
	db := newConfigSourceTestDB(t)
	inst := seedConfigSourceInstance(t, db, "srv-inline", "old")
	instanceSvc := NewInstanceService(db, NewGroupService(db), nil)
	defer instanceSvc.Shutdown()
	svc := NewConfigSourceService(db, instanceSvc, nil)

	v := "new-cmd -Xmx1G"
	_, err := svc.UpdateSource(inst.ID, []SurfaceUpdateItem{
		{ItemKey: ConfigItemStartupCommand, Source: model.ConfigSourceInline, InlineValue: &v},
	}, 7)
	require.NoError(t, err)

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, "new-cmd -Xmx1G", got.StartCommand)

	var row model.InstanceConfigSource
	require.NoError(t, db.Where("instance_id = ? AND item_key = ?", inst.ID, ConfigItemStartupCommand).First(&row).Error)
	require.Equal(t, model.ConfigSourceInline, row.Source)
	require.Equal(t, "new-cmd -Xmx1G", row.InlineValue)
}

func TestConfigSource_FileModeRegistersAndPreviews(t *testing.T) {
	db := newConfigSourceTestDB(t)
	inst := seedConfigSourceInstance(t, db, "srv-file", "cmd")
	svc := NewConfigSourceService(db, nil, nil)
	svc.SetPreviewForTest(func(_ *model.Instance, _ string) (string, map[string]string, error) {
		return "query.port=25566\n", map[string]string{"query.port": "25566"}, nil
	})

	_, err := svc.UpdateSource(inst.ID, []SurfaceUpdateItem{
		{ItemKey: ConfigItemPropsPrefix + "query.port", Source: model.ConfigSourceFile, FilePath: defaultServerPropertiesPath, FileKey: "query.port"},
	}, 0)
	require.NoError(t, err)

	var row model.InstanceConfigSource
	require.NoError(t, db.Where("instance_id = ? AND item_key = ?", inst.ID, ConfigItemPropsPrefix+"query.port").First(&row).Error)
	require.Equal(t, model.ConfigSourceFile, row.Source)
	require.Empty(t, row.InlineValue, "切为文件引用后平台不再持有内联值")

	items, err := svc.Surface(inst.ID)
	require.NoError(t, err)
	qp := indexSurface(items)[ConfigItemPropsPrefix+"query.port"]
	require.Equal(t, model.ConfigSourceFile, qp.Source)
	require.Equal(t, "25566", qp.EffectiveValue)
	require.False(t, qp.Editable)
}

func TestConfigSource_FileToInlineUsesFileValueAsInitial(t *testing.T) {
	db := newConfigSourceTestDB(t)
	inst := seedConfigSourceInstance(t, db, "srv-migrate", "cmd")
	svc := NewConfigSourceService(db, nil, nil)
	svc.SetPreviewForTest(func(_ *model.Instance, _ string) (string, map[string]string, error) {
		return "", map[string]string{"view-distance": "12"}, nil
	})

	prev := model.InstanceConfigSource{
		InstanceID: inst.ID, ItemKey: ConfigItemPropsPrefix + "view-distance",
		Source: model.ConfigSourceFile, FilePath: defaultServerPropertiesPath, FileKey: "view-distance",
	}
	v, err := svc.resolveInlineValue(inst, SurfaceUpdateItem{ItemKey: ConfigItemPropsPrefix + "view-distance", Source: model.ConfigSourceInline}, prev, true)
	require.NoError(t, err)
	require.Equal(t, "12", v, "file→inline 应以文件现值为初值")
}

func TestConfigSource_ConflictWarningsForManagedFile(t *testing.T) {
	db := newConfigSourceTestDB(t)
	inst := seedConfigSourceInstance(t, db, "srv-conflict", "cmd")
	require.NoError(t, db.Create(&model.InstanceConfigSource{
		InstanceID: inst.ID, ItemKey: ConfigItemPropsPrefix + "query.port",
		Source: model.ConfigSourceFile, FilePath: defaultServerPropertiesPath, FileKey: "query.port",
	}).Error)
	svc := NewConfigSourceService(db, nil, nil)

	warns := svc.ConflictWarnings(inst.ID, defaultServerPropertiesPath, []string{"query.port", "motd"})
	require.Len(t, warns, 1)
	require.Equal(t, "query.port", warns[0]["key"])
	require.Equal(t, "warning", warns[0]["level"])
}

func TestConfigSource_InlinePropValues(t *testing.T) {
	db := newConfigSourceTestDB(t)
	inst := seedConfigSourceInstance(t, db, "srv-ports", "cmd")
	require.NoError(t, db.Create(&model.InstanceConfigSource{
		InstanceID: inst.ID, ItemKey: ConfigItemPropsPrefix + "query.port",
		Source: model.ConfigSourceInline, InlineValue: "25600",
	}).Error)
	svc := NewConfigSourceService(db, nil, nil)

	got := svc.InlinePropValues(inst.ID, []string{"server-port", "query.port"})
	require.Equal(t, "25600", got["query.port"])
	_, ok := got["server-port"]
	require.False(t, ok)
}

func TestMergeInlinePorts(t *testing.T) {
	cfg := schema.ParsedConfig{
		Path:   "instance=1:server.properties",
		Fields: []*workerpb.ConfigField{{Key: "server-port", Value: "25565"}},
	}
	merged := mergeInlinePorts(cfg, map[string]string{"query.port": "25600", "server-port": ""})
	require.Equal(t, "25565", mergedField(merged, "server-port"), "空内联值不覆盖文件值")
	require.Equal(t, "25600", mergedField(merged, "query.port"), "内联端口并入校验输入")
}

func TestIsManagedItemKey(t *testing.T) {
	require.True(t, isManagedItemKey(ConfigItemStartupCommand))
	require.True(t, isManagedItemKey(ConfigItemStartupLaunchSpec))
	require.True(t, isManagedItemKey(ConfigItemPropsPrefix+"enable-query"))
	require.True(t, isManagedItemKey(ConfigItemPropsPrefix+"query.port"))
	require.True(t, isManagedItemKey(ConfigItemPropsPrefix+"view-distance"))
	require.False(t, isManagedItemKey(ConfigItemPropsPrefix+"bogus-key"))
	require.False(t, isManagedItemKey("flags.jvm"))
}
