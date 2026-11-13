package router

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
)

const (
	advancedQueryMaxRows   = 1000
	advancedQueryMaxBytes  = 32 << 20
	advancedQueryTimeoutMS = 30000
	advancedQueryMaxFanout = 64
	logQueryStartedContext = "logs.query.started"
)

// Only advanced mode adds these explicit HTTP caps. Ordinary queries keep the
// existing coordinator/VL/export budgets. A request may tighten caps, never
// enlarge them; full export still applies its configured row/byte gate.
func advancedQueryBudget(c *gin.Context, budget logcoord.QueryBudget) (logcoord.QueryBudget, error) {
	maxBytes, err := boundedLogQueryParam(c, "maxBytes", advancedQueryMaxBytes)
	if err != nil {
		return budget, err
	}
	timeout, err := boundedLogQueryParam(c, "timeoutMs", advancedQueryTimeoutMS)
	if err != nil {
		return budget, err
	}
	fanout, err := boundedLogQueryParam(c, "maxFanout", advancedQueryMaxFanout)
	if err != nil {
		return budget, err
	}
	budget.MaxBytes = maxBytes
	budget.TimeoutMS = uint32(timeout)
	budget.MaxFanout = uint32(fanout)
	isWholeExport := c.Request.Method == "POST" && c.Query("range") != "page"
	if !isWholeExport && budget.Limit > advancedQueryMaxRows {
		budget.Limit = advancedQueryMaxRows
	}
	if budget.Limit <= 0 {
		budget.Limit = logcoord.DefaultPageLimit
	}
	return budget, nil
}

func boundedLogQueryParam(c *gin.Context, name string, ceiling uint64) (uint64, error) {
	raw := c.Query(name)
	if raw == "" {
		return ceiling, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s 必须是正整数，且不超过 %d", name, ceiling)
	}
	if value > ceiling {
		value = ceiling
	}
	return value, nil
}

type logQueryMetadata struct {
	ElapsedMS    float64 `json:"elapsed_ms"`
	ReturnedRows int     `json:"returned_rows"`
	Limit        int     `json:"limit"`
	MaxBytes     uint64  `json:"max_bytes"`
	TimeoutMS    uint32  `json:"timeout_ms"`
	MaxFanout    uint32  `json:"max_fanout"`
}

func queryMetadata(c *gin.Context, q logcoord.Query, rows int) *logQueryMetadata {
	if !parseBoolParam(c.Query("advanced")) {
		return nil
	}
	started, _ := c.Get(logQueryStartedContext)
	start, _ := started.(time.Time)
	elapsed := float64(0)
	if !start.IsZero() {
		elapsed = float64(time.Since(start).Microseconds()) / 1000
	}
	meta := &logQueryMetadata{ElapsedMS: elapsed, ReturnedRows: rows, Limit: q.Budget.Limit,
		MaxBytes: q.Budget.MaxBytes, TimeoutMS: q.Budget.TimeoutMS, MaxFanout: q.Budget.MaxFanout}
	return meta
}

func auditAdvancedLogQuery(c *gin.Context) {
	if !parseBoolParam(c.Query("advanced")) {
		c.Next()
		return
	}
	started := time.Now()
	c.Next()
	value, _ := c.Get("logs.query.scope")
	q, _ := value.(logcoord.Query)
	principal := q.PrincipalKey
	if principal == "" {
		if access := getAccess(c); access != nil {
			principal = fmt.Sprintf("user:%d", access.UserID)
		}
	}
	// Query text can contain credentials or personal data. Record a content
	// fingerprint, not raw input, including for rejected expressions.
	digest := sha256.Sum256([]byte(c.Query("logsQL")))
	slog.Info("高级实例日志查询结束", "principal", principal, "scope", q.PermissionScope,
		"querySHA256", fmt.Sprintf("%x", digest), "queryBytes", len(c.Query("logsQL")),
		"route", c.FullPath(), "httpStatus", c.Writer.Status(), "responseBytes", c.Writer.Size(),
		"cancelled", c.Request.Context().Err() != nil, "elapsedMs", time.Since(started).Milliseconds(),
		"from", q.FromUTC, "to", q.ToUTC, "maxBytes", q.Budget.MaxBytes,
		"timeoutMs", q.Budget.TimeoutMS, "maxFanout", q.Budget.MaxFanout)
}
