package service

import (
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// DBBrowseService 是数据库资源管理器（FR-084）的只读元数据/行查询服务。
//
// 它在 Control Plane 自身的 *gorm.DB 上提供「表清单 + 分页行浏览」，严守架构不变量
// 「数据库仅 Control Plane 可读写」——只发 SELECT，无任何写/改/删。
// 表名/列名一律经白名单（必须命中 Migrator 列举的真实表/列）后才进入 GORM 查询构造，
// 过滤值作为参数化绑定，不拼接用户输入到 SQL，杜绝注入。
// 敏感列（密码哈希/密钥/token 等）的值在服务端统一打码，原文不进入响应。
type DBBrowseService struct {
	db *gorm.DB
}

// NewDBBrowseService 创建数据库浏览服务。
func NewDBBrowseService(db *gorm.DB) *DBBrowseService {
	return &DBBrowseService{db: db}
}

// 行查询分页/排序硬上限，防止单次拉取拖垮内存或 DB（大表分页不卡）。
const (
	dbBrowseDefaultPageSize = 50
	dbBrowseMaxPageSize     = 200
)

// maskedValue 是敏感列脱敏后的占位文本。
const maskedValue = "******"

// sensitiveColumnTokens 是判定敏感列的子串集合（列名不区分大小写，命中即脱敏）。
// 宁可多打码不可漏：覆盖各表既有的密码/密钥/token/盐等列（如 users.password_hash、
// nodes.node_secret、client_pull_keys.key_hash、client_versions.sign_* 等）。
var sensitiveColumnTokens = []string{
	"password", "passwd", "secret", "token", "node_secret",
	"private_key", "priv_key", "sign_priv", "salt",
	"api_key", "access_key", "credential", "pull_key", "key_hash",
}

// isSensitiveColumn 判断列名是否敏感（应脱敏其值）。
func isSensitiveColumn(name string) bool {
	lower := strings.ToLower(name)
	for _, tok := range sensitiveColumnTokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// DBColumn 描述一列（名称 / 数据库类型 / 是否敏感）。
type DBColumn struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Sensitive bool   `json:"sensitive"`
}

// DBTableInfo 是表清单中的一项（表名 + 行数）。
type DBTableInfo struct {
	Name     string `json:"name"`
	RowCount int64  `json:"rowCount"`
}

// DBRowsParams 是行查询的请求参数（已由 handler 解析）。
type DBRowsParams struct {
	Page         int
	PageSize     int
	Sort         string // 排序列名；非该表列则忽略
	Order        string // asc | desc
	FilterColumn string // 过滤列名；非该表列或与 FilterValue 不成对则忽略
	FilterValue  string // 过滤关键字（LIKE %value%）
}

// DBRowsResult 是行查询结果。
type DBRowsResult struct {
	Table    string                   `json:"table"`
	Columns  []DBColumn               `json:"columns"`
	Rows     []map[string]interface{} `json:"rows"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"pageSize"`
	Total    int64                    `json:"total"`
}

// ErrTableNotFound 表示请求的表名不在白名单（不存在）。
var ErrTableNotFound = fmt.Errorf("table not found")

// Tables 列出 CP 数据库全部表及其行数。
func (s *DBBrowseService) Tables() ([]DBTableInfo, error) {
	names, err := s.db.Migrator().GetTables()
	if err != nil {
		return nil, fmt.Errorf("列举数据库表失败: %w", err)
	}
	// GetTables 顺序依赖驱动；按名排序保证稳定展示。
	sort.Strings(names)
	out := make([]DBTableInfo, 0, len(names))
	for _, name := range names {
		var count int64
		// 表名来自 Migrator 列举（可信标识符），仅用于 COUNT；无用户输入。
		if err := s.db.Table(name).Count(&count).Error; err != nil {
			// 单表计数失败不致整体失败，记 -1 表示未知。
			count = -1
		}
		out = append(out, DBTableInfo{Name: name, RowCount: count})
	}
	return out, nil
}

// tableColumns 返回表的列定义（含敏感标记）。表名须已校验存在。
func (s *DBBrowseService) tableColumns(table string) ([]DBColumn, error) {
	types, err := s.db.Migrator().ColumnTypes(table)
	if err != nil {
		return nil, fmt.Errorf("读取表 %s 列失败: %w", table, err)
	}
	cols := make([]DBColumn, 0, len(types))
	for _, ct := range types {
		name := ct.Name()
		cols = append(cols, DBColumn{
			Name:      name,
			Type:      strings.ToLower(ct.DatabaseTypeName()),
			Sensitive: isSensitiveColumn(name),
		})
	}
	return cols, nil
}

// TableRows 分页查询某表的行（敏感列脱敏）。表名经白名单校验，排序/过滤列经列白名单校验。
func (s *DBBrowseService) TableRows(table string, p DBRowsParams) (*DBRowsResult, error) {
	// 表名白名单：必须命中真实表清单，否则视为不存在（拒绝任意标识符）。
	if !s.tableExists(table) {
		return nil, ErrTableNotFound
	}

	cols, err := s.tableColumns(table)
	if err != nil {
		return nil, err
	}
	colSet := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		colSet[c.Name] = struct{}{}
	}

	page := p.Page
	if page < 1 {
		page = 1
	}
	pageSize := p.PageSize
	if pageSize <= 0 {
		pageSize = dbBrowseDefaultPageSize
	}
	if pageSize > dbBrowseMaxPageSize {
		pageSize = dbBrowseMaxPageSize
	}

	// 基础查询 + 可选过滤（列名校验后用反引号包裹，值参数化绑定）。
	base := s.db.Table(table)
	if p.FilterColumn != "" && p.FilterValue != "" {
		if _, ok := colSet[p.FilterColumn]; ok {
			base = base.Where(quoteIdent(p.FilterColumn)+" LIKE ?", "%"+p.FilterValue+"%")
		}
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计表 %s 行数失败: %w", table, err)
	}

	q := base
	// 排序列校验后才应用；非法列忽略（回退默认顺序）。
	if p.Sort != "" {
		if _, ok := colSet[p.Sort]; ok {
			dir := "ASC"
			if strings.EqualFold(p.Order, "desc") {
				dir = "DESC"
			}
			q = q.Order(quoteIdent(p.Sort) + " " + dir)
		}
	}

	var rows []map[string]interface{}
	if err := q.Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询表 %s 行失败: %w", table, err)
	}

	maskRows(rows, cols)

	return &DBRowsResult{
		Table:    table,
		Columns:  cols,
		Rows:     rows,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}, nil
}

// tableExists 判断表是否在白名单（真实存在）。
func (s *DBBrowseService) tableExists(table string) bool {
	return s.db.Migrator().HasTable(table)
}

// maskRows 就地将敏感列的非空值替换为打码占位。
func maskRows(rows []map[string]interface{}, cols []DBColumn) {
	sensitive := make(map[string]struct{})
	for _, c := range cols {
		if c.Sensitive {
			sensitive[c.Name] = struct{}{}
		}
	}
	if len(sensitive) == 0 {
		return
	}
	for _, row := range rows {
		for name := range sensitive {
			if v, ok := row[name]; ok && v != nil {
				row[name] = maskedValue
			}
		}
	}
}

// quoteIdent 用反引号包裹标识符（仅用于已通过白名单校验的列名）。
// 反引号在 SQLite 与 MySQL 均合法；调用方保证 name 来自列白名单，故无注入面。
func quoteIdent(name string) string {
	return "`" + name + "`"
}
