package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// migrationHarness 存量迁移测试基座（FR-348）：Asset + 渠道 + 任务 + 迁移服务共库同根。
type migrationHarness struct {
	assets    *AssetService
	channels  *ArtifactStorageChannelService
	tasks     *TaskService
	migration *ArtifactMigrationService
	root      *dataroot.Root
	db        *gorm.DB
}

func newMigrationHarness(t *testing.T) *migrationHarness {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Asset{}, &model.ArtifactStorageChannel{}, &model.Node{},
		&model.Task{}, &model.TaskLog{},
		&model.ArtifactMigration{}, &model.ArtifactMigrationFailure{},
	))
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)

	channels := NewArtifactStorageChannelService(db, root)
	enc, eerr := newKeyEncryptor(DevKeyEncSecretBase64)
	require.NoError(t, eerr)
	channels.SetKeyEncryptor(enc)
	require.NoError(t, channels.EnsureBuiltin())

	assets := NewAssetService(db, root)
	assets.SetStorageChannels(channels)
	tasks := NewTaskService(db)
	migration := NewArtifactMigrationService(db, root, channels, tasks)
	return &migrationHarness{assets: assets, channels: channels, tasks: tasks, migration: migration, root: root, db: db}
}

// createS3Channel 建 s3 渠道（prefix=jm，不自动设活跃——迁移目标不要求活跃）。
func (h *migrationHarness) createS3Channel(t *testing.T, name, endpoint string) *model.ArtifactStorageChannel {
	t.Helper()
	useSSL := false
	ch, err := h.channels.Create(SaveArtifactStorageParams{
		Name: name, Type: "s3", Endpoint: endpoint, Bucket: "jm-artifacts",
		Prefix: "jm", AccessKey: "ak", SecretKey: "sk", UseSSL: &useSSL,
	})
	require.NoError(t, err)
	return ch
}

// ingestClientFile 以当前活跃渠道入库一个 client-file 制品。
func (h *migrationHarness) ingestClientFile(t *testing.T, filename string, data []byte) *model.Asset {
	t.Helper()
	a, err := h.assets.Ingest(bytes.NewReader(data), IngestParams{Type: model.AssetTypeClientFile, Filename: filename})
	require.NoError(t, err)
	return a
}

// waitTerminal 等任务进入终态（迁移在后台 goroutine 执行）。
func (h *migrationHarness) waitTerminal(t *testing.T, taskID string) model.Task {
	t.Helper()
	var task model.Task
	require.Eventually(t, func() bool {
		if err := h.db.Where("task_id = ?", taskID).First(&task).Error; err != nil {
			return false
		}
		return task.State.IsTerminal()
	}, 15*time.Second, 20*time.Millisecond, "任务未在期限内终态")
	return task
}

// reloadAsset 重新读取制品记录。
func (h *migrationHarness) reloadAsset(t *testing.T, id uint) model.Asset {
	t.Helper()
	var a model.Asset
	require.NoError(t, h.db.First(&a, id).Error)
	return a
}

// registryOf 读取迁移登记行（实时计数）。
func (h *migrationHarness) registryOf(t *testing.T, taskID string) model.ArtifactMigration {
	t.Helper()
	var reg model.ArtifactMigration
	require.NoError(t, h.db.Where("task_id = ?", taskID).First(&reg).Error)
	return reg
}

// failableFakeS3 可注入故障的假 S3（path-style /bucket/key）：
// rejectPut/rejectDelete 命中键返回 500；blockPut 非 nil 时 CAS 键 PUT 先阻塞等释放，
// putStarted 通知测试「CAS PUT 已在途」（并发 409 / 强停时序编排用）。
type failableFakeS3 struct {
	mu           sync.Mutex
	objects      map[string][]byte
	putsByKey    map[string]int
	rejectPut    func(key string) bool
	rejectDelete func(key string) bool
	blockPut     chan struct{}
	putStarted   chan string
}

func newFailableFakeS3(t *testing.T) (*failableFakeS3, *httptest.Server) {
	t.Helper()
	f := &failableFakeS3{
		objects:    map[string][]byte{},
		putsByKey:  map[string]int{},
		putStarted: make(chan string, 16),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/"))
		isCAS := strings.Contains(key, "var/artifacts/")
		switch r.Method {
		case http.MethodPut:
			if isCAS {
				select {
				case f.putStarted <- key:
				default:
				}
				f.mu.Lock()
				block := f.blockPut
				f.mu.Unlock()
				if block != nil {
					<-block // 等测试释放（探测键不受阻）。
				}
			}
			f.mu.Lock()
			reject := f.rejectPut != nil && f.rejectPut(key)
			f.mu.Unlock()
			body, _ := io.ReadAll(r.Body)
			if reject {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			f.mu.Lock()
			f.objects[key] = body
			f.putsByKey[key]++
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			f.mu.Lock()
			b, ok := f.objects[key]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodHead:
			f.mu.Lock()
			b, ok := f.objects[key]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			f.mu.Lock()
			reject := f.rejectDelete != nil && f.rejectDelete(key)
			if !reject {
				delete(f.objects, key)
			}
			f.mu.Unlock()
			if reject {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// objectKey 目标对象全键 = bucket/prefix/CAS 相对路径。
func objectKey(relPath string) string { return "jm-artifacts/jm/" + relPath }

// TestArtifactMigration_LocalToS3_MigratesAllAndDeletesSource
// local→s3 全量迁移：逐条记录翻 s3+渠道 ID+external、对象上到目标、本地 CAS 删除；
// 非 client-file 类型不参与；计数 total=migrated；任务 succeeded、进度 100。
func TestArtifactMigration_LocalToS3_MigratesAllAndDeletesSource(t *testing.T) {
	h := newMigrationHarness(t)
	fake, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "rustfs", srv.URL)

	payloads := [][]byte{[]byte("blob-a"), []byte("blob-b"), []byte("blob-c")}
	assets := make([]*model.Asset, 0, len(payloads))
	for i, p := range payloads {
		assets = append(assets, h.ingestClientFile(t, "f"+strconv.Itoa(i)+".bin", p))
	}
	core, err := h.assets.Ingest(bytes.NewReader([]byte("core-jar")), IngestParams{Type: model.AssetTypeCore, Filename: "paper.jar"})
	require.NoError(t, err)

	taskID, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State)
	require.Equal(t, 100, task.Progress)

	for i, a := range assets {
		got := h.reloadAsset(t, a.ID)
		require.Equal(t, model.AssetBackendS3, got.StorageBackend)
		require.Equal(t, target.ID, got.StorageChannelID)
		require.Equal(t, model.AssetStorageExternal, got.StorageState)
		require.Equal(t, a.RelPath, got.RelPath, "RelPath 跨后端不变（ADR-073）")
		require.Equal(t, payloads[i], fake.objects[objectKey(a.RelPath)], "对象内容一致")
		_, serr := os.Stat(h.root.Abs(a.RelPath))
		require.True(t, os.IsNotExist(serr), "本地源文件已删")
	}
	gotCore := h.reloadAsset(t, core.ID)
	require.Equal(t, model.AssetBackendLocal, gotCore.StorageBackend, "非 client-file 不参与迁移")
	require.FileExists(t, h.root.Abs(gotCore.RelPath))

	reg := h.registryOf(t, taskID)
	require.Equal(t, 3, reg.Total)
	require.Equal(t, 3, reg.Migrated)
	require.Zero(t, reg.Failed)
	require.Zero(t, reg.Skipped)

	status, serr := h.migration.Latest()
	require.NoError(t, serr)
	require.NotNil(t, status.Task)
	require.Equal(t, taskID, status.Task.TaskID)
	require.NotNil(t, status.Migration)
	require.Equal(t, target.ID, status.Migration.TargetChannelID)
	require.Equal(t, "rustfs", status.Migration.TargetName)
	require.Equal(t, 3, status.Migration.Migrated)
}

// TestArtifactMigration_FailedItemKeepsSourceAndResumes
// 单条失败（目标 PUT 拒）：该条记录不变、源文件保留、失败明细落库；其余条正常迁；任务 failed。
// 重新发起同目标：已迁条跳过不重传、失败条重试成功 → succeeded（幂等续跑）。
func TestArtifactMigration_FailedItemKeepsSourceAndResumes(t *testing.T) {
	h := newMigrationHarness(t)
	fake, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "rustfs", srv.URL)

	good := h.ingestClientFile(t, "good.bin", []byte("good-bytes"))
	bad := h.ingestClientFile(t, "bad.bin", []byte("bad-bytes"))
	badKey := objectKey(bad.RelPath)
	fake.mu.Lock()
	fake.rejectPut = func(key string) bool { return key == badKey }
	fake.mu.Unlock()

	taskID, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "1 条")

	gotGood := h.reloadAsset(t, good.ID)
	require.Equal(t, model.AssetBackendS3, gotGood.StorageBackend)
	gotBad := h.reloadAsset(t, bad.ID)
	require.Equal(t, model.AssetBackendLocal, gotBad.StorageBackend, "失败条记录不变")
	require.EqualValues(t, 0, gotBad.StorageChannelID)
	require.FileExists(t, h.root.Abs(bad.RelPath), "失败条不删源")

	fails, ferr := h.migration.Failures(taskID)
	require.NoError(t, ferr)
	require.Len(t, fails, 1)
	require.Equal(t, bad.SHA256, fails[0].SHA256)
	require.Equal(t, bad.ID, fails[0].AssetID)
	require.Contains(t, fails[0].Reason, "写入目标失败")

	reg := h.registryOf(t, taskID)
	require.Equal(t, 2, reg.Total)
	require.Equal(t, 1, reg.Migrated)
	require.Equal(t, 1, reg.Failed)

	// 续跑：解除故障重新发起 → 已迁条跳过（PUT 计数不涨）、失败条补迁成功。
	fake.mu.Lock()
	fake.rejectPut = nil
	goodPuts := fake.putsByKey[objectKey(good.RelPath)]
	fake.mu.Unlock()

	taskID2, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	task2 := h.waitTerminal(t, taskID2)
	require.Equal(t, model.TaskStateSucceeded, task2.State)

	gotBad = h.reloadAsset(t, bad.ID)
	require.Equal(t, model.AssetBackendS3, gotBad.StorageBackend)
	require.Equal(t, target.ID, gotBad.StorageChannelID)
	_, serr := os.Stat(h.root.Abs(bad.RelPath))
	require.True(t, os.IsNotExist(serr), "补迁成功后删源")

	fake.mu.Lock()
	require.Equal(t, goodPuts, fake.putsByKey[objectKey(good.RelPath)], "已迁条跳过不重传")
	fake.mu.Unlock()

	reg2 := h.registryOf(t, taskID2)
	require.Equal(t, 2, reg2.Total)
	require.Equal(t, 1, reg2.Migrated)
	require.Equal(t, 1, reg2.Skipped, "已迁条计入跳过")
	require.Zero(t, reg2.Failed)
}

// TestArtifactMigration_Sha256Mismatch_FailsWithoutSourceDelete
// 源内容被篡改（sha256 不符）：该条失败「校验不符」、不删源、记录不更新。
func TestArtifactMigration_Sha256Mismatch_FailsWithoutSourceDelete(t *testing.T) {
	h := newMigrationHarness(t)
	_, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "rustfs", srv.URL)

	a := h.ingestClientFile(t, "tamper.bin", []byte("original"))
	// 同长度改写内容：绕过 size 防线，命中 sha256 复核。
	require.NoError(t, os.WriteFile(h.root.Abs(a.RelPath), []byte("tampered"), 0o644))

	taskID, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateFailed, task.State)

	got := h.reloadAsset(t, a.ID)
	require.Equal(t, model.AssetBackendLocal, got.StorageBackend, "记录不更新")
	require.FileExists(t, h.root.Abs(a.RelPath), "不删源")

	fails, ferr := h.migration.Failures(taskID)
	require.NoError(t, ferr)
	require.Len(t, fails, 1)
	require.Contains(t, fails[0].Reason, "校验不符")
}

// TestArtifactMigration_ConcurrentStart_Conflict 在途迁移未终态时再发起 → ErrArtifactMigrationInFlight。
func TestArtifactMigration_ConcurrentStart_Conflict(t *testing.T) {
	h := newMigrationHarness(t)
	fake, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "rustfs", srv.URL)
	h.ingestClientFile(t, "big.bin", []byte("payload"))

	gate := make(chan struct{})
	fake.mu.Lock()
	fake.blockPut = gate
	fake.mu.Unlock()

	taskID, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	<-fake.putStarted // CAS PUT 已在途，任务确凿非终态。

	_, err = h.migration.Start(target.ID, 1)
	require.ErrorIs(t, err, ErrArtifactMigrationInFlight)

	close(gate)
	h.waitTerminal(t, taskID)
}

// TestArtifactMigration_RecoverOrphans_AllowsRestart
// CP 重启孤儿（DB 滞留 running、无 goroutine）：清扫前发起被 409 挡；RecoverOrphans 置 failed 后可重新发起。
func TestArtifactMigration_RecoverOrphans_AllowsRestart(t *testing.T) {
	h := newMigrationHarness(t)
	_, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "rustfs", srv.URL)
	h.ingestClientFile(t, "f.bin", []byte("payload"))

	orphanID := uuid.New().String()
	_, err := h.tasks.CreateTask(orphanID, 0, model.TaskKindArtifactMigrate, "制品存量迁移 → rustfs", "", 1)
	require.NoError(t, err)
	require.NoError(t, h.tasks.MarkRunning(orphanID))

	_, err = h.migration.Start(target.ID, 1)
	require.ErrorIs(t, err, ErrArtifactMigrationInFlight, "孤儿未清扫前视同在途")

	require.NoError(t, h.migration.RecoverOrphans())
	var orphan model.Task
	require.NoError(t, h.db.Where("task_id = ?", orphanID).First(&orphan).Error)
	require.Equal(t, model.TaskStateFailed, orphan.State)
	require.Contains(t, orphan.Error, "重新发起")

	taskID, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State)
}

// TestArtifactMigration_TargetProbeFailed_Rejected 目标真连探测失败 → 发起被拒、不建任务。
func TestArtifactMigration_TargetProbeFailed_Rejected(t *testing.T) {
	h := newMigrationHarness(t)
	_, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "dead", srv.URL)
	srv.Close() // 渠道故障态。

	_, err := h.migration.Start(target.ID, 1)
	require.ErrorIs(t, err, ErrArtifactMigrationTargetUnavailable)

	var count int64
	require.NoError(t, h.db.Model(&model.Task{}).Count(&count).Error)
	require.Zero(t, count, "探测失败不建任务")
}

// TestArtifactMigration_StartUnknownChannel_NotFound 目标渠道不存在 → 404 语义错误。
func TestArtifactMigration_StartUnknownChannel_NotFound(t *testing.T) {
	h := newMigrationHarness(t)
	_, err := h.migration.Start(9999, 1)
	require.ErrorIs(t, err, ErrArtifactStorageNotFound)
}

// TestArtifactMigration_S3ToLocal_Roundtrip s3→local 回迁：记录回 local/0/hot、CAS 文件就位、
// S3 源对象删除；活跃渠道（仍为 s3）不受迁移影响。
func TestArtifactMigration_S3ToLocal_Roundtrip(t *testing.T) {
	h := newMigrationHarness(t)
	fake, srv := newFailableFakeS3(t)
	s3ch := h.createS3Channel(t, "rustfs", srv.URL)
	_, err := h.channels.SetActive(s3ch.ID)
	require.NoError(t, err)

	data := []byte("roundtrip-bytes")
	a := h.ingestClientFile(t, "r.bin", data)
	require.Equal(t, model.AssetBackendS3, a.StorageBackend, "前置：入库已落 s3")

	var builtin model.ArtifactStorageChannel
	require.NoError(t, h.db.Where("builtin = ?", true).First(&builtin).Error)

	taskID, err := h.migration.Start(builtin.ID, 1)
	require.NoError(t, err)
	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State)

	got := h.reloadAsset(t, a.ID)
	require.Equal(t, model.AssetBackendLocal, got.StorageBackend)
	require.EqualValues(t, 0, got.StorageChannelID, "local 语义渠道引用归零")
	require.Equal(t, model.AssetStorageHot, got.StorageState)
	fileBytes, rerr := os.ReadFile(h.root.Abs(a.RelPath))
	require.NoError(t, rerr)
	require.Equal(t, data, fileBytes, "CAS 文件就位且内容一致")
	fake.mu.Lock()
	_, remains := fake.objects[objectKey(a.RelPath)]
	fake.mu.Unlock()
	require.False(t, remains, "S3 源对象已删")

	active, aerr := h.channels.Active()
	require.NoError(t, aerr)
	require.Equal(t, s3ch.ID, active.ID, "迁移不动活跃渠道（写路径独立）")
}

// TestArtifactMigration_SkipAlreadyAtTarget 已在目标渠道的记录跳过（不重传），混合分布计数正确。
func TestArtifactMigration_SkipAlreadyAtTarget(t *testing.T) {
	h := newMigrationHarness(t)
	fake, srv := newFailableFakeS3(t)
	s3ch := h.createS3Channel(t, "rustfs", srv.URL)

	// 先在 s3 活跃下入一条（已在目标），再切回本机入一条 local。
	_, err := h.channels.SetActive(s3ch.ID)
	require.NoError(t, err)
	onTarget := h.ingestClientFile(t, "on-target.bin", []byte("already-there"))
	var builtin model.ArtifactStorageChannel
	require.NoError(t, h.db.Where("builtin = ?", true).First(&builtin).Error)
	_, err = h.channels.SetActive(builtin.ID)
	require.NoError(t, err)
	local := h.ingestClientFile(t, "local.bin", []byte("to-migrate"))

	fake.mu.Lock()
	putsBefore := fake.putsByKey[objectKey(onTarget.RelPath)]
	fake.mu.Unlock()

	taskID, err := h.migration.Start(s3ch.ID, 1)
	require.NoError(t, err)
	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State)

	reg := h.registryOf(t, taskID)
	require.Equal(t, 2, reg.Total)
	require.Equal(t, 1, reg.Migrated)
	require.Equal(t, 1, reg.Skipped)
	require.Zero(t, reg.Failed)

	fake.mu.Lock()
	require.Equal(t, putsBefore, fake.putsByKey[objectKey(onTarget.RelPath)], "已在目标不重传")
	fake.mu.Unlock()
	require.Equal(t, model.AssetBackendS3, h.reloadAsset(t, local.ID).StorageBackend)
}

// TestArtifactMigration_SourceDeleteFailure_StillMigrated
// 删源失败（S3 DELETE 拒）：记录已先行更新（先改记录再删源的顺序保证）、该条仍计成功、任务 succeeded；
// 源残留归 FR-349 对账。
func TestArtifactMigration_SourceDeleteFailure_StillMigrated(t *testing.T) {
	h := newMigrationHarness(t)
	fake, srv := newFailableFakeS3(t)
	s3ch := h.createS3Channel(t, "rustfs", srv.URL)
	_, err := h.channels.SetActive(s3ch.ID)
	require.NoError(t, err)
	a := h.ingestClientFile(t, "sticky.bin", []byte("sticky-bytes"))

	fake.mu.Lock()
	fake.rejectDelete = func(key string) bool { return strings.Contains(key, "var/artifacts/") }
	fake.mu.Unlock()

	var builtin model.ArtifactStorageChannel
	require.NoError(t, h.db.Where("builtin = ?", true).First(&builtin).Error)
	taskID, err := h.migration.Start(builtin.ID, 1)
	require.NoError(t, err)
	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State, "删源失败不判失败（记录已指向新位置）")

	got := h.reloadAsset(t, a.ID)
	require.Equal(t, model.AssetBackendLocal, got.StorageBackend)
	require.FileExists(t, h.root.Abs(a.RelPath), "目标副本已就位")
	fake.mu.Lock()
	_, remains := fake.objects[objectKey(a.RelPath)]
	fake.mu.Unlock()
	require.True(t, remains, "源对象删除失败残留（FR-349 对账收口）")
	require.Equal(t, 1, h.registryOf(t, taskID).Migrated)
}

// TestArtifactMigration_DeleteChannelGuard_WhileInFlight 迁移在途时任何渠道禁删；终态后恢复可删。
func TestArtifactMigration_DeleteChannelGuard_WhileInFlight(t *testing.T) {
	h := newMigrationHarness(t)
	fake, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "rustfs", srv.URL)
	bystander := h.createS3Channel(t, "bystander", srv.URL)
	h.ingestClientFile(t, "f.bin", []byte("payload"))

	gate := make(chan struct{})
	fake.mu.Lock()
	fake.blockPut = gate
	fake.mu.Unlock()

	taskID, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	<-fake.putStarted

	err = h.channels.Delete(bystander.ID)
	require.ErrorIs(t, err, ErrArtifactStorageMigrationInFlight)

	close(gate)
	h.waitTerminal(t, taskID)
	require.NoError(t, h.channels.Delete(bystander.ID), "终态后恢复可删")
}

// TestArtifactMigration_CancelStopsLoopAndResumes 强停后循环停在下一条；重新发起续跑补齐。
func TestArtifactMigration_CancelStopsLoopAndResumes(t *testing.T) {
	h := newMigrationHarness(t)
	fake, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "rustfs", srv.URL)
	a1 := h.ingestClientFile(t, "a1.bin", []byte("first"))
	a2 := h.ingestClientFile(t, "a2.bin", []byte("second"))
	a3 := h.ingestClientFile(t, "a3.bin", []byte("third"))

	gate := make(chan struct{})
	fake.mu.Lock()
	fake.blockPut = gate
	fake.mu.Unlock()

	taskID, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	<-fake.putStarted // 第一条 PUT 在途。

	require.NoError(t, h.tasks.Cancel(nil, taskID)) // NodeID=0 → 直接置 canceled。
	close(gate)                                     // 放行在途条，其完成后循环应检测到取消并停。

	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateCanceled, task.State)

	require.Eventually(t, func() bool {
		return h.reloadAsset(t, a1.ID).StorageBackend == model.AssetBackendS3
	}, 15*time.Second, 20*time.Millisecond, "在途条完成收尾")
	require.Equal(t, model.AssetBackendLocal, h.reloadAsset(t, a2.ID).StorageBackend, "后续条未动")
	require.Equal(t, model.AssetBackendLocal, h.reloadAsset(t, a3.ID).StorageBackend)

	// 等 goroutine 完全退出（登记行不再变化）后续跑补齐。
	require.Eventually(t, func() bool {
		var n int64
		h.db.Model(&model.Task{}).Where("kind = ? AND state IN ?", model.TaskKindArtifactMigrate, []model.TaskState{model.TaskStatePending, model.TaskStateRunning}).Count(&n)
		return n == 0
	}, 15*time.Second, 20*time.Millisecond)

	taskID2, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	task2 := h.waitTerminal(t, taskID2)
	require.Equal(t, model.TaskStateSucceeded, task2.State)
	reg2 := h.registryOf(t, taskID2)
	require.Equal(t, 3, reg2.Total)
	require.Equal(t, 2, reg2.Migrated)
	require.Equal(t, 1, reg2.Skipped)
	require.Equal(t, model.AssetBackendS3, h.reloadAsset(t, a2.ID).StorageBackend)
	require.Equal(t, model.AssetBackendS3, h.reloadAsset(t, a3.ID).StorageBackend)
}

// TestArtifactMigration_LatestEmpty 从未迁移过：Latest 返回双 nil（前端「无迁移历史」态）。
func TestArtifactMigration_LatestEmpty(t *testing.T) {
	h := newMigrationHarness(t)
	status, err := h.migration.Latest()
	require.NoError(t, err)
	require.Nil(t, status.Task)
	require.Nil(t, status.Migration)
}

// TestArtifactMigration_NothingToMigrate 全部已在目标：无 eligible 条，任务直接 succeeded、全计跳过。
func TestArtifactMigration_NothingToMigrate(t *testing.T) {
	h := newMigrationHarness(t)
	_, srv := newFailableFakeS3(t)
	target := h.createS3Channel(t, "rustfs", srv.URL)
	_, err := h.channels.SetActive(target.ID)
	require.NoError(t, err)
	h.ingestClientFile(t, "done.bin", []byte("already"))

	taskID, err := h.migration.Start(target.ID, 1)
	require.NoError(t, err)
	task := h.waitTerminal(t, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State)
	require.Equal(t, 100, task.Progress)
	reg := h.registryOf(t, taskID)
	require.Equal(t, 1, reg.Total)
	require.Zero(t, reg.Migrated)
	require.Equal(t, 1, reg.Skipped)
}
