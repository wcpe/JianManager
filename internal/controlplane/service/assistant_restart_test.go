package service

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 凭据跨重启有效性（验收项，此前「只在单进程内验证过」）。
//
// 用**文件型** SQLite 而不是内存库：内存库在进程结束后就没了，那样测到的只是
// 「同一进程内两次访问同一连接」——与重启毫无关系。跨重启的实质是
// 「密文落了盘、主密钥还在、新进程能解开」，必须让数据真的离开进程。
//
// 另一件必须守住的事：**密钥换掉时密文不能被清空**。若实现选择「解密失败就清空重录」，
// 那么一次密钥配置失误就会把所有供应商凭据永久毁掉，而重录需要每个供应商的原始密钥——
// 那是不可恢复的损失。因此失败路径断言「密文原样还在」。

// newFileDB 在临时目录建一个文件型库（t.TempDir 由测试框架回收）。
func newFileDB(t *testing.T, name string) (*gorm.DB, string) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), name+".db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开文件库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AssistantProvider{}, &model.AssistantModel{}, &model.AssistantSetting{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db, dsn
}

// openExistingDB 用**新的连接**打开同一个库文件（模拟新进程）。
func openExistingDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("重新打开库失败: %v", err)
	}
	return db
}

// newServiceOn 在给定库上构造服务（出站放行回环，便于后续探测）。
func newServiceOn(t *testing.T, db *gorm.DB, enc *KeyEncryptor) *AssistantService {
	t.Helper()
	s := NewAssistantService(db, enc)
	s.SetEgress(httpclient.NewGuard(httpclient.GuardOptions{AllowInsecure: true}))
	if err := s.EnsureSetting(); err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
	return s
}

func TestCredentialSurvivesRestart(t *testing.T) {
	db, dsn := newFileDB(t, "restart")
	enc := testEncryptor(t)
	const secret = "sk-survives-restart-123456"

	// 进程 A：写入 stored 凭据。
	sA := newServiceOn(t, db, enc)
	created, err := sA.CreateProvider(SaveProviderParams{
		Name:             "重启验证",
		Protocol:         model.AssistantProtocolOpenAIChat,
		BaseURL:          "https://api.example.com/v1",
		Enabled:          true,
		CredentialMode:   model.AssistantCredentialStored,
		CredentialSecret: secret,
	})
	if err != nil {
		t.Fatalf("写入凭据失败: %v", err)
	}

	// 进程 B：新连接、同一库、同一把主密钥。
	dbB := openExistingDB(t, dsn)
	sB := newServiceOn(t, dbB, enc)
	view, err := sB.GetProvider(created.ID)
	if err != nil {
		t.Fatalf("重启后读取供应商失败: %v", err)
	}
	if !view.HasCredential {
		t.Fatal("重启后应仍报告已配置凭据")
	}
	if view.CredentialMode != model.AssistantCredentialStored {
		t.Fatalf("模式应保持 stored，实际 %q", view.CredentialMode)
	}

	// 关键断言：真的能解密回原文（而不是「hasCredential 为真但解不开」）。
	// 直接走 resolveCredential——它就是探测与运行循环实际使用的那个函数。
	var row model.AssistantProvider
	if err := dbB.First(&row, created.ID).Error; err != nil {
		t.Fatalf("读取原始行失败: %v", err)
	}
	plain, err := sB.resolveCredential(&row)
	if err != nil {
		t.Fatalf("重启后解密失败: %v", err)
	}
	if plain != secret {
		t.Fatalf("解密结果与原文不一致：期望 %q 实际 %q", secret, plain)
	}
}

func TestCredentialKeyChangeKeepsCiphertext(t *testing.T) {
	db, dsn := newFileDB(t, "keychange")
	encA := testEncryptor(t)

	sA := newServiceOn(t, db, encA)
	created, err := sA.CreateProvider(SaveProviderParams{
		Name: "换密钥验证", Protocol: model.AssistantProtocolOpenAIChat,
		BaseURL: "https://api.example.com/v1", Enabled: true,
		CredentialMode: model.AssistantCredentialStored, CredentialSecret: "sk-original-value",
	})
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	var before model.AssistantProvider
	if err := db.First(&before, created.ID).Error; err != nil {
		t.Fatalf("读取原始行失败: %v", err)
	}
	if before.CredentialEnc == "" {
		t.Fatal("应已写入密文")
	}
	cipherBefore := before.CredentialEnc

	// 换一把不同的主密钥（模拟运维误换了 JIANMANAGER_CLIENT_KEY_ENC_SECRET）。
	// 必须是**合法的 32 字节**密钥：长度不对会构造失败，那样这个用例会被 skip，
	// 而「skip 掉的失败路径断言」等于没测。
	another := mustEncryptorFrom(t, "WFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFg=")
	if another == nil {
		t.Fatal("第二把密钥构造失败——用例不应依赖 skip，否则关切的路径根本没测到")
	}
	dbB := openExistingDB(t, dsn)
	sB := newServiceOn(t, dbB, another)

	var row model.AssistantProvider
	if err := dbB.First(&row, created.ID).Error; err != nil {
		t.Fatalf("读取原始行失败: %v", err)
	}
	// 解密应失败，且错误要能指向「主密钥变了」这一真实原因。
	if _, err := sB.resolveCredential(&row); err == nil {
		t.Fatal("换密钥后不应解密成功")
	}

	// 关键断言：密文**原样保留**。解密失败不得顺手清空——
	// 清空等于把「密钥配错」升级成「全部凭据永久损失」。
	var after model.AssistantProvider
	if err := dbB.First(&after, created.ID).Error; err != nil {
		t.Fatalf("复查失败: %v", err)
	}
	if after.CredentialEnc != cipherBefore {
		t.Fatalf("密文不应被改写：before=%q after=%q", cipherBefore, after.CredentialEnc)
	}
	if after.CredentialMode != model.AssistantCredentialStored {
		t.Fatalf("模式不应被降级，实际 %q", after.CredentialMode)
	}
}

// mustEncryptorFrom 用给定 base64 密钥构造加密器（失败返回 nil，由调用方 skip）。
func mustEncryptorFrom(t *testing.T, secretB64 string) *KeyEncryptor {
	t.Helper()
	enc, err := newKeyEncryptor(secretB64)
	if err != nil || enc == nil {
		return nil
	}
	return enc
}
