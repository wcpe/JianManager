package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// dbBrowseTestRow 是行查询测试用的轻量表，含一个敏感列（node_secret）与可排序/过滤列。
type dbBrowseTestRow struct {
	ID         uint   `gorm:"primaryKey"`
	Name       string `gorm:"type:varchar(64)"`
	NodeSecret string `gorm:"column:node_secret;type:varchar(128)"`
}

func (dbBrowseTestRow) TableName() string { return "db_browse_test_rows" }

func newDBBrowseTestService(t *testing.T) (*DBBrowseService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &dbBrowseTestRow{}))
	// cache=shared 共享同库，逐用例清表。
	require.NoError(t, db.Exec("DELETE FROM users").Error)
	require.NoError(t, db.Exec("DELETE FROM db_browse_test_rows").Error)
	return NewDBBrowseService(db), db
}

// isSensitiveColumn 对密码/密钥/token 等列判敏感，普通列不判敏感。
func TestIsSensitiveColumn(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"password_hash", true},
		{"Password", true},
		{"secret", true},
		{"node_secret", true},
		{"refresh_token", true},
		{"key_hash", true},
		{"sign_priv_key", true},
		{"pull_key", true},
		{"username", false},
		{"id", false},
		{"created_at", false},
		{"name", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isSensitiveColumn(tt.name))
		})
	}
}

// maskRows 仅替换敏感列的非空值，保留 null 与非敏感列原值。
func TestMaskRows(t *testing.T) {
	cols := []DBColumn{
		{Name: "id", Sensitive: false},
		{Name: "username", Sensitive: false},
		{Name: "password", Sensitive: true},
	}
	rows := []map[string]interface{}{
		{"id": int64(1), "username": "admin", "password": "$2a$hash"},
		{"id": int64(2), "username": "bob", "password": nil},
	}
	maskRows(rows, cols)
	require.Equal(t, maskedValue, rows[0]["password"])
	require.Equal(t, "admin", rows[0]["username"])
	require.Equal(t, int64(1), rows[0]["id"])
	// null 敏感值保持 null，不替换为占位。
	require.Nil(t, rows[1]["password"])
}

// Tables 列出全部已迁移表并给出行数（含测试表与 users）。
func TestTables(t *testing.T) {
	svc, db := newDBBrowseTestService(t)
	require.NoError(t, db.Create(&dbBrowseTestRow{Name: "a", NodeSecret: "s1"}).Error)
	require.NoError(t, db.Create(&dbBrowseTestRow{Name: "b", NodeSecret: "s2"}).Error)

	tables, err := svc.Tables()
	require.NoError(t, err)

	got := map[string]int64{}
	for _, ti := range tables {
		got[ti.Name] = ti.RowCount
	}
	require.Contains(t, got, "db_browse_test_rows")
	require.Contains(t, got, "users")
	require.Equal(t, int64(2), got["db_browse_test_rows"])
}

// TableRows 对敏感列脱敏，列定义带 sensitive 标记，分页字段正确。
func TestTableRows_MasksSensitiveColumns(t *testing.T) {
	svc, db := newDBBrowseTestService(t)
	require.NoError(t, db.Create(&dbBrowseTestRow{Name: "alpha", NodeSecret: "topsecret"}).Error)

	res, err := svc.TableRows("db_browse_test_rows", DBRowsParams{})
	require.NoError(t, err)
	require.Equal(t, "db_browse_test_rows", res.Table)
	require.Equal(t, int64(1), res.Total)
	require.Equal(t, 1, res.Page)
	require.Equal(t, dbBrowseDefaultPageSize, res.PageSize)
	require.Len(t, res.Rows, 1)

	// node_secret 列标记敏感且值被打码；name 列正常。
	var secretCol, nameCol *DBColumn
	for i := range res.Columns {
		switch res.Columns[i].Name {
		case "node_secret":
			secretCol = &res.Columns[i]
		case "name":
			nameCol = &res.Columns[i]
		}
	}
	require.NotNil(t, secretCol)
	require.True(t, secretCol.Sensitive)
	require.NotNil(t, nameCol)
	require.False(t, nameCol.Sensitive)
	require.Equal(t, maskedValue, res.Rows[0]["node_secret"])
	require.Equal(t, "alpha", res.Rows[0]["name"])
}

// TableRows 分页：pageSize 钳制在上限内，Offset 正确切片。
func TestTableRows_Pagination(t *testing.T) {
	svc, db := newDBBrowseTestService(t)
	for i := 0; i < 5; i++ {
		require.NoError(t, db.Create(&dbBrowseTestRow{Name: "n", NodeSecret: "s"}).Error)
	}

	// 每页 2 行，取第 2 页 → 2 行。
	res, err := svc.TableRows("db_browse_test_rows", DBRowsParams{Page: 2, PageSize: 2})
	require.NoError(t, err)
	require.Equal(t, int64(5), res.Total)
	require.Len(t, res.Rows, 2)
	require.Equal(t, 2, res.Page)
	require.Equal(t, 2, res.PageSize)

	// pageSize 越界被钳制到 dbBrowseMaxPageSize。
	res2, err := svc.TableRows("db_browse_test_rows", DBRowsParams{PageSize: 99999})
	require.NoError(t, err)
	require.Equal(t, dbBrowseMaxPageSize, res2.PageSize)
}

// TableRows 排序：合法列降序生效；非法排序列被忽略（不报错）。
func TestTableRows_Sort(t *testing.T) {
	svc, db := newDBBrowseTestService(t)
	require.NoError(t, db.Create(&dbBrowseTestRow{ID: 1, Name: "a", NodeSecret: "s"}).Error)
	require.NoError(t, db.Create(&dbBrowseTestRow{ID: 2, Name: "b", NodeSecret: "s"}).Error)
	require.NoError(t, db.Create(&dbBrowseTestRow{ID: 3, Name: "c", NodeSecret: "s"}).Error)

	res, err := svc.TableRows("db_browse_test_rows", DBRowsParams{Sort: "id", Order: "desc"})
	require.NoError(t, err)
	require.Len(t, res.Rows, 3)
	require.EqualValues(t, 3, res.Rows[0]["id"])
	require.EqualValues(t, 1, res.Rows[2]["id"])

	// 非法排序列：不报错、回退默认顺序。
	resBad, err := svc.TableRows("db_browse_test_rows", DBRowsParams{Sort: "not_a_col; DROP TABLE users", Order: "desc"})
	require.NoError(t, err)
	require.Len(t, resBad.Rows, 3)
}

// TableRows 过滤：合法列 LIKE 命中子集；非法过滤列被忽略（返回全集，不报错）。
func TestTableRows_Filter(t *testing.T) {
	svc, db := newDBBrowseTestService(t)
	require.NoError(t, db.Create(&dbBrowseTestRow{Name: "apple", NodeSecret: "s"}).Error)
	require.NoError(t, db.Create(&dbBrowseTestRow{Name: "banana", NodeSecret: "s"}).Error)
	require.NoError(t, db.Create(&dbBrowseTestRow{Name: "apricot", NodeSecret: "s"}).Error)

	res, err := svc.TableRows("db_browse_test_rows", DBRowsParams{FilterColumn: "name", FilterValue: "ap"})
	require.NoError(t, err)
	require.Equal(t, int64(2), res.Total) // apple, apricot
	require.Len(t, res.Rows, 2)

	// 非法过滤列被忽略 → 返回全集。
	resBad, err := svc.TableRows("db_browse_test_rows", DBRowsParams{FilterColumn: "evil", FilterValue: "x"})
	require.NoError(t, err)
	require.Equal(t, int64(3), resBad.Total)
}

// TableRows 对不存在/非白名单表名返回 ErrTableNotFound（拒绝任意标识符）。
func TestTableRows_UnknownTableRejected(t *testing.T) {
	svc, _ := newDBBrowseTestService(t)
	_, err := svc.TableRows("no_such_table", DBRowsParams{})
	require.ErrorIs(t, err, ErrTableNotFound)

	// 注入式表名同样被白名单拒绝。
	_, err = svc.TableRows("users; DROP TABLE users", DBRowsParams{})
	require.ErrorIs(t, err, ErrTableNotFound)
}

// 用户表内置 password 列被识别为敏感并打码（端到端覆盖真实 model）。
func TestTableRows_UserPasswordMasked(t *testing.T) {
	svc, db := newDBBrowseTestService(t)
	require.NoError(t, db.Create(&model.User{Username: "admin", Password: "hashed", Role: model.RolePlatformAdmin}).Error)

	res, err := svc.TableRows("users", DBRowsParams{})
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	require.Equal(t, maskedValue, res.Rows[0]["password"])
	require.Equal(t, "admin", res.Rows[0]["username"])
}
