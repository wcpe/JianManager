package vlsup

import (
	"context"
	"errors"
)

// ErrMetadataReadBudget means a bounded metadata response could not be read in full.
// No extra byte is read to probe EOF beyond the granted allowance.
var ErrMetadataReadBudget = errors.New("vlsup: metadata read budget exceeded")

type metadataReadBudgetKey struct{}

// WithMetadataReadBudget lets the query owner charge actual field_names/field_values
// HTTP reads to one request ledger. The callback reserves once per physical POST;
// its refund receives actual body bytes on every exit path. Other endpoints and
// callers without this opt-in context retain their existing behavior.
func WithMetadataReadBudget(ctx context.Context, reserve func() (uint64, func(uint64), error)) context.Context {
	return context.WithValue(ctx, metadataReadBudgetKey{}, reserve)
}
