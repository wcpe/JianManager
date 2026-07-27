package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func setupTransferTicketDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AgentToken{}, &model.Instance{}, &model.Node{}, &model.AgentTransferTicket{}))
	return db
}

// seedTransferPrincipal 建节点+实例，并签发一个持 instance.content 的 V2 Token。
func seedTransferPrincipal(t *testing.T, db *gorm.DB) (*AgentTokenService, *AgentPrincipal, *model.Instance) {
	t.Helper()
	node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "s1", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "i1", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)

	svc := NewAgentTokenService(db)
	_, plain, err := svc.Issue(IssueAgentTokenRequest{
		Name:                 "content-agent",
		ScopedInstanceIDs:    []uint{inst.ID},
		PolicyVersion:        AgentPolicyVersionV2,
		CapabilitiesProvided: true,
		Capabilities:         []string{AgentCapabilityInstanceContent},
		CreatedBy:            1,
	})
	require.NoError(t, err)
	p, err := svc.Authenticate(plain)
	require.NoError(t, err)
	return svc, p, inst
}

func TestAgentTransferTicket_IssueAndConsume(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	tickets, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)

	ticket, expiresAt, err := tickets.Issue(p, inst.ID, AgentTransferDirectionUpload, "plugins/foo.jar")
	require.NoError(t, err)
	require.NotEmpty(t, ticket)
	assert.WithinDuration(t, time.Now().Add(agentTransferTicketTTL), expiresAt, time.Minute)

	claims, err := tickets.Consume(ticket)
	require.NoError(t, err)
	assert.Equal(t, p.TokenID, claims.TokenID)
	assert.Equal(t, inst.ID, claims.InstanceID)
	assert.Equal(t, AgentTransferDirectionUpload, claims.Direction)
	assert.Equal(t, "plugins/foo.jar", claims.Path)
}

func TestAgentTransferTicket_SingleUse(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	tickets, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)

	ticket, _, err := tickets.Issue(p, inst.ID, AgentTransferDirectionDownload, "logs/latest.log")
	require.NoError(t, err)
	_, err = tickets.Consume(ticket)
	require.NoError(t, err)
	_, err = tickets.Consume(ticket)
	assert.ErrorIs(t, err, ErrAgentTransferTicketInvalid, "票据不得二次消费")
}

func TestAgentTransferTicket_ConsumptionPersistsAcrossServiceInstances(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	issuer, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)
	ticket, _, err := issuer.Issue(p, inst.ID, AgentTransferDirectionDownload, "logs/latest.log")
	require.NoError(t, err)
	_, err = issuer.Consume(ticket)
	require.NoError(t, err)
	verifier, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)
	_, err = verifier.Consume(ticket)
	assert.ErrorIs(t, err, ErrAgentTransferTicketInvalid)
}

func TestAgentTransferTicket_Expired(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	now := time.Now()
	clock := func() time.Time { return now }
	tickets, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, func() time.Time { return clock() })
	require.NoError(t, err)

	ticket, _, err := tickets.Issue(p, inst.ID, AgentTransferDirectionUpload, "plugins/foo.jar")
	require.NoError(t, err)

	base := now
	clock = func() time.Time { return base.Add(agentTransferTicketTTL + time.Second) }
	_, err = tickets.Consume(ticket)
	assert.ErrorIs(t, err, ErrAgentTransferTicketInvalid, "过期票据必须拒绝")
}

func TestAgentTransferTicket_TamperedPathRejected(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	tickets, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)

	ticket, _, err := tickets.Issue(p, inst.ID, AgentTransferDirectionUpload, "plugins/foo.jar")
	require.NoError(t, err)

	// 改一个字符即签名失配（篡改 path 必然改变 payload 段）。
	tampered := ticket[:len(ticket)-2] + "AA"
	_, err = tickets.Consume(tampered)
	assert.ErrorIs(t, err, ErrAgentTransferTicketInvalid, "篡改票据必须拒绝")
}

func TestAgentTransferTicket_ForeignSecretRejected(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	issuer, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)
	verifier, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("other-master")), agentSvc, nil)
	require.NoError(t, err)

	ticket, _, err := issuer.Issue(p, inst.ID, AgentTransferDirectionUpload, "plugins/foo.jar")
	require.NoError(t, err)
	_, err = verifier.Consume(ticket)
	assert.ErrorIs(t, err, ErrAgentTransferTicketInvalid, "他方密钥签发的票据必须拒绝")
}

func TestAgentTransferTicket_RevokedTokenRejected(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	tickets, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)

	ticket, _, err := tickets.Issue(p, inst.ID, AgentTransferDirectionUpload, "plugins/foo.jar")
	require.NoError(t, err)
	require.NoError(t, agentSvc.Revoke(p.TokenID))

	_, err = tickets.Consume(ticket)
	assert.ErrorIs(t, err, ErrAgentTransferTicketInvalid, "Token 吊销后票据必须失效")
}

func TestAgentTransferTicket_InstanceOwnershipRevalidated(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	tickets, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)

	ticket, _, err := tickets.Issue(p, inst.ID, AgentTransferDirectionUpload, "plugins/foo.jar")
	require.NoError(t, err)

	// 归属变化：实例移出 Token scope（这里直接删除实例记录模拟不可解析）。
	require.NoError(t, db.Delete(&model.Instance{}, inst.ID).Error)
	_, err = tickets.Consume(ticket)
	assert.ErrorIs(t, err, ErrAgentTransferTicketInvalid, "实例归属重验失败必须拒绝")
}

func TestAgentTransferTicket_IssueRejectsBadInput(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	tickets, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)

	_, _, err = tickets.Issue(p, inst.ID, "sideways", "plugins/foo.jar")
	assert.Error(t, err, "非法 direction 必须拒绝")

	_, _, err = tickets.Issue(p, inst.ID, AgentTransferDirectionUpload, "../../etc/passwd")
	assert.Error(t, err, "路径穿越必须拒绝")

	_, _, err = tickets.Issue(p, inst.ID, AgentTransferDirectionUpload, "")
	assert.Error(t, err, "空路径必须拒绝")

	// scope 外实例
	_, _, err = tickets.Issue(p, inst.ID+999, AgentTransferDirectionUpload, "plugins/foo.jar")
	assert.ErrorIs(t, err, ErrAgentForbidden, "scope 外实例必须收敛为 forbidden")
}

func TestAgentTransferTicket_DirectionBound(t *testing.T) {
	db := setupTransferTicketDB(t)
	agentSvc, p, inst := seedTransferPrincipal(t, db)
	tickets, err := NewAgentTransferTicketService(DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)

	ticket, _, err := tickets.Issue(p, inst.ID, AgentTransferDirectionUpload, "plugins/foo.jar")
	require.NoError(t, err)
	claims, err := tickets.Consume(ticket)
	require.NoError(t, err)
	// 端点据此拒绝：上传票据不可用于下载。
	assert.NotEqual(t, AgentTransferDirectionDownload, claims.Direction)
}
