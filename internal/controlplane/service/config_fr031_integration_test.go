package service

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	workergrpc "github.com/wcpe/JianManager/internal/worker/grpc"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestFR031ConfigService_GrpcWorkerVersionDiffRollbackAndCrossCheck(t *testing.T) {
	db := newConfigTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Instance{}); err != nil {
		t.Fatalf("迁移节点和实例失败: %v", err)
	}

	const nodeUUID = "node-fr031"
	const instanceUUID = "inst-fr031-a"
	workDir := t.TempDir()
	seedContent := "# 主端口\nserver-port=25565\nonline-mode=true\nmotd=Old MOTD\nmax-players=20\n"
	if err := os.WriteFile(filepath.Join(workDir, "server.properties"), []byte(seedContent), 0644); err != nil {
		t.Fatalf("写入初始配置失败: %v", err)
	}

	pool := cpgrpc.NewClientPool()
	conn := newFR031WorkerConn(t, nodeUUID, instanceUUID, workDir)
	pool.SetWorkerClientForTest(nodeUUID, workerpb.NewWorkerServiceClient(conn))

	node := &model.Node{UUID: nodeUUID, Name: "fr031-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s"}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("插入节点失败: %v", err)
	}
	inst := &model.Instance{UUID: instanceUUID, NodeID: node.ID, Name: "survival", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "java -jar server.jar", WorkDir: workDir}
	if err := db.Create(inst).Error; err != nil {
		t.Fatalf("插入实例失败: %v", err)
	}
	// 同节点其它实例只需提供最新版本内容，跨实例校验会聚合它触发端口冲突。
	sibling := &model.Instance{UUID: "inst-fr031-b", NodeID: node.ID, Name: "creative", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "java -jar server.jar", WorkDir: t.TempDir()}
	if err := db.Create(sibling).Error; err != nil {
		t.Fatalf("插入同节点实例失败: %v", err)
	}
	if err := db.Create(&model.InstanceConfigVersion{InstanceID: sibling.ID, FilePath: "server.properties", Content: "server-port=25565\nonline-mode=false\n", ContentHash: "sibling"}).Error; err != nil {
		t.Fatalf("插入同节点配置版本失败: %v", err)
	}

	svc := NewConfigService(db, pool)
	read, err := svc.Read(inst.ID, "server.properties")
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if read.Model != "server.properties" || !strings.Contains(read.SchemaJSON, "Minecraft Java") {
		t.Fatalf("应由 CP 在 Worker 响应上叠加内置 schema，实际 model=%q schema=%s", read.Model, read.SchemaJSON)
	}
	assertFR031Field(t, read.Fields, "server-port", "25565", "int", 2)
	assertFR031Field(t, read.Fields, "online-mode", "true", "bool", 3)

	patchedID, validation, err := svc.WriteFields(inst.ID, "server.properties", map[string]string{
		"motd":        "Edited MOTD",
		"max-players": "32",
	}, "表单保存", 7)
	if err != nil {
		t.Fatalf("表单字段保存失败: %v", err)
	}
	if patchedID == 0 || validation["valid"] != true {
		t.Fatalf("表单保存应生成有效版本，version=%d validation=%+v", patchedID, validation)
	}
	patchedBytes, err := os.ReadFile(filepath.Join(workDir, "server.properties"))
	if err != nil {
		t.Fatalf("读取保存后文件失败: %v", err)
	}
	patchedContent := string(patchedBytes)
	if !strings.Contains(patchedContent, "# 主端口\nserver-port=25565\nonline-mode=true\nmotd=Edited MOTD\nmax-players=32") {
		t.Fatalf("字段补丁应保留注释和原键顺序，实际内容:\n%s", patchedContent)
	}

	secondContent := strings.Replace(patchedContent, "motd=Edited MOTD", "motd=Next MOTD", 1)
	secondID, _, err := svc.Write(inst.ID, "server.properties", secondContent, "文本保存", 8, nil)
	if err != nil {
		t.Fatalf("文本保存失败: %v", err)
	}
	versions, err := svc.Versions(inst.ID, "server.properties")
	if err != nil {
		t.Fatalf("查询版本失败: %v", err)
	}
	if len(versions) != 2 || versions[0].ID != secondID || versions[1].ID != patchedID {
		t.Fatalf("版本应按 ID 倒序返回，实际 %+v", versions)
	}

	diff, err := svc.Diff(inst.ID, "server.properties", patchedID, secondID)
	if err != nil {
		t.Fatalf("生成 diff 失败: %v", err)
	}
	if !strings.Contains(diff.UnifiedDiff, "-motd=Edited MOTD") || !strings.Contains(diff.UnifiedDiff, "+motd=Next MOTD") {
		t.Fatalf("diff 应展示 MOTD 变更，实际:\n%s", diff.UnifiedDiff)
	}

	rollbackID, err := svc.Rollback(inst.ID, "server.properties", patchedID, "回滚到表单版本", 9)
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	rolledBytes, err := os.ReadFile(filepath.Join(workDir, "server.properties"))
	if err != nil {
		t.Fatalf("读取回滚后文件失败: %v", err)
	}
	if string(rolledBytes) != patchedContent {
		t.Fatalf("回滚后文件应恢复到目标版本内容，实际:\n%s", string(rolledBytes))
	}
	versions, err = svc.Versions(inst.ID, "server.properties")
	if err != nil {
		t.Fatalf("查询回滚后版本失败: %v", err)
	}
	if versions[0].ID != rollbackID || versions[0].RollbackOfVersionID == nil || *versions[0].RollbackOfVersionID != patchedID {
		t.Fatalf("回滚应生成带来源版本的新版本，实际 %+v", versions[0])
	}

	issues, err := svc.CheckCrossFile(inst.ID, "server.properties", patchedContent)
	if err != nil {
		t.Fatalf("跨实例一致性校验失败: %v", err)
	}
	if !containsFR031Issue(issues, "端口 25565 重复") {
		t.Fatalf("应检出同节点重复端口，实际 %+v", issues)
	}
}

func newFR031WorkerConn(t *testing.T, nodeUUID, instanceUUID, workDir string) *grpc.ClientConn {
	t.Helper()
	pm := process.NewManager(t.TempDir())
	if err := pm.Create(instanceUUID, "survival", "java -jar server.jar", "", workDir, nil, false, process.ProcessTypeDirect, "", "", 0, 0); err != nil {
		t.Fatalf("注册 Worker 实例失败: %v", err)
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(server, workergrpc.NewServer(pm, nodeUUID, nil, nil, nil))
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///fr031-worker",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("创建 gRPC 客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func assertFR031Field(t *testing.T, fields []map[string]any, key, value, typ string, line int32) {
	t.Helper()
	for _, f := range fields {
		if f["key"] != key {
			continue
		}
		if f["value"] != value || f["type"] != typ || f["line"] != line {
			t.Fatalf("字段 %s 不符合期望: %+v", key, f)
		}
		return
	}
	t.Fatalf("未找到字段 %s，全部字段: %+v", key, fields)
}

func containsFR031Issue(issues []map[string]any, needle string) bool {
	for _, issue := range issues {
		msg, _ := issue["message"].(string)
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
