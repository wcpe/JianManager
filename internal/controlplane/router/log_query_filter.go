package router

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// HTTP expressions are filter-only. VL parses full field/filter syntax; this
// inexpensive boundary check protects our event-row/view/aggregation contract
// without mistaking a quoted regex alternation for a pipe operator.
const advancedFilterMaxLen = 8 * 1024

func federationFilter(c *gin.Context) (string, error) {
	advanced := parseBoolParam(c.Query("advanced"))
	expression := strings.TrimSpace(c.Query("keyword"))
	source := strings.ToLower(strings.TrimSpace(c.Query("source")))
	if advanced {
		if expression != "" {
			return "", fmt.Errorf("高级模式请只使用 logsQL，不要同时提供 keyword")
		}
		if c.Query("view") == "platform" || (source != "" && source != "instance") {
			return "", fmt.Errorf("高级 LogsQL 仅适用于实例日志；平台与节点日志请使用文本查询")
		}
		expression = strings.TrimSpace(c.Query("logsQL"))
		if expression == "" {
			return "", fmt.Errorf("高级 LogsQL 不能为空；查询全部实例日志请使用 *")
		}
		// Even administrators cannot accidentally query node process logs through
		// the instance-only workbench. Target authorization is still separate.
		source = "instance"
	} else if c.Query("logsQL") != "" {
		return "", fmt.Errorf("logsQL 需要显式启用 advanced 模式")
	}
	if err := validateLogFilterExpression(expression); err != nil {
		return "", err
	}

	terms := make([]string, 0, 2)
	if level := strings.ToLower(strings.TrimSpace(c.Query("level"))); isFederationLevel(level) {
		terms = append(terms, "level:"+strings.ToUpper(level))
	}
	if prefix, ok := federationSourcePrefix(source); ok {
		terms = append(terms, `log_source_id:"`+prefix+`"`)
	}
	if len(terms) == 0 {
		return expression, nil
	}
	joined := strings.Join(terms, " AND ")
	if expression == "" {
		return joined, nil
	}
	return "(" + expression + ") AND " + joined, nil
}

// validateLogFilterExpression deliberately does not reimplement LogsQL. It
// validates framing only (quotes/groups/ranges, no pipes/comments/control bytes)
// so both keyword and logsQL are safe to combine with structured filters. VL
// receives the user expression in extra_filters and the authorized selector in
// query; a valid OR or _time condition can only narrow that intersection.
func validateLogFilterExpression(expression string) error {
	if len(expression) > advancedFilterMaxLen {
		return fmt.Errorf("LogsQL 过滤表达式超过 %d 字节，请缩短条件", advancedFilterMaxLen)
	}
	if !utf8.ValidString(expression) {
		return fmt.Errorf("LogsQL 过滤表达式不是有效 UTF-8")
	}
	type group struct {
		open  byte
		comma bool
	}
	var groups []group
	var quote byte
	for i := 0; i < len(expression); i++ {
		ch := expression[i]
		if ch < 0x20 && ch != '\t' && ch != '\n' && ch != '\r' {
			return fmt.Errorf("LogsQL 过滤表达式含不支持的控制字符")
		}
		if quote != 0 {
			if ch == '\\' && quote != '`' && i+1 < len(expression) {
				// Escapes are part of a quoted string, never structural syntax.
				i++
				if expression[i] < 0x20 && expression[i] != '\t' && expression[i] != '\n' && expression[i] != '\r' {
					return fmt.Errorf("LogsQL 过滤表达式含不支持的控制字符")
				}
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'', '`':
			quote = ch
		case '|':
			return fmt.Errorf("此入口仅支持 LogsQL 过滤条件，不支持管道；统计请使用分析区")
		case '#':
			return fmt.Errorf("LogsQL 过滤条件不支持注释；请把 # 放在引号内")
		case '/':
			if i+1 < len(expression) && (expression[i+1] == '/' || expression[i+1] == '*') {
				return fmt.Errorf("LogsQL 过滤条件不支持注释；请把文本放在引号内")
			}
		case '(', '[', '{':
			if len(groups) >= 64 {
				return fmt.Errorf("LogsQL 括号嵌套超过 64 层")
			}
			groups = append(groups, group{open: ch})
		case ',':
			if len(groups) > 0 {
				groups[len(groups)-1].comma = true
			}
		case ')', ']', '}':
			if len(groups) == 0 {
				return fmt.Errorf("LogsQL 括号不配对")
			}
			last := groups[len(groups)-1]
			matched := (last.open == '(' && ch == ')') || (last.open == '[' && ch == ']') || (last.open == '{' && ch == '}')
			// LogsQL ranges may mix inclusivity: [min,max) and (min,max].
			if last.comma && ((last.open == '[' && ch == ')') || (last.open == '(' && ch == ']')) {
				matched = true
			}
			if !matched {
				return fmt.Errorf("LogsQL 括号不配对")
			}
			groups = groups[:len(groups)-1]
		}
	}
	if quote != 0 {
		return fmt.Errorf("LogsQL 引号不配对")
	}
	if len(groups) > 0 {
		return fmt.Errorf("LogsQL 括号不配对")
	}
	return nil
}
