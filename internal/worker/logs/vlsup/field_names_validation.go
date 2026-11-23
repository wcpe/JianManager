package vlsup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type fieldNamesValidationKey struct{}

// FieldNamesValidation controls opt-in row-level presence evidence. VL v1.52
// field_names counts matching block rows for every column header, including
// columns empty in all matching rows. Those names alone cannot prove presence.
type FieldNamesValidation struct {
	Allowed   func(string) bool
	MaxFields int
	OnlyField string
	Truncated func()
}

// WithFieldNamesValidation requires actual non-empty values in the same scoped
// rows before exposing a candidate name. Verification uses the caller's metadata
// byte/fanout ledger and absolute deadline, including all batched count queries.
func WithFieldNamesValidation(ctx context.Context, validation FieldNamesValidation) context.Context {
	return context.WithValue(ctx, fieldNamesValidationKey{}, validation)
}

func (c *Client) validateFieldNames(ctx context.Context, params url.Values, names []string, validation FieldNamesValidation) ([]string, error) {
	candidates := make([]string, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		if validation.Allowed == nil || !validation.Allowed(name) || seen[name] {
			continue
		}
		if validation.OnlyField != "" && name != validation.OnlyField {
			continue
		}
		seen[name] = true
		candidates = append(candidates, name)
	}
	sort.Strings(candidates)
	if validation.MaxFields > 0 && len(candidates) > validation.MaxFields {
		candidates = candidates[:validation.MaxFields]
		if validation.Truncated != nil {
			validation.Truncated()
		}
	}
	var out []string
	for len(candidates) > 0 {
		batch := 0
		var terms []string
		p := make(url.Values, len(params))
		for key, values := range params {
			p[key] = append([]string(nil), values...)
		}
		base := params.Get("query")
		for batch < len(candidates) {
			field := candidates[batch]
			if strings.ContainsAny(field, ".-") {
				field = strconv.Quote(field)
			}
			term := fmt.Sprintf("count() if (NOT %s:=\"\") as __jm_field_%d", field, batch)
			next := append(append([]string(nil), terms...), term)
			p.Set("query", base+" | stats "+strings.Join(next, ", "))
			// Bound the actual encoded request too; field lengths alone do not
			// bound URL expansion or the fixed authorization selector's size.
			if len(p.Encode()) > 12<<10 {
				break
			}
			terms = next
			batch++
		}
		if batch == 0 {
			return nil, ErrMetadataReadBudget
		}
		p.Set("query", base+" | stats "+strings.Join(terms, ", "))
		body, err := c.Post(ctx, "/select/logsql/query", p)
		if err != nil {
			return nil, err
		}
		var counts map[string]json.RawMessage
		if err := json.Unmarshal(body, &counts); err != nil {
			return nil, fmt.Errorf("vlsup: field presence response: %w", err)
		}
		for i, name := range candidates[:batch] {
			raw, ok := counts[fmt.Sprintf("__jm_field_%d", i)]
			if !ok {
				return nil, fmt.Errorf("vlsup: field presence count missing for %q", name)
			}
			text := strings.Trim(string(raw), "\"")
			count, err := strconv.ParseUint(text, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("vlsup: invalid field presence count: %w", err)
			}
			if count > 0 {
				out = append(out, name)
			}
		}
		candidates = candidates[batch:]
	}
	return out, nil
}
