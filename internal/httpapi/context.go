package httpapi

import (
	"context"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type principalKey struct{}

func withPrincipal(ctx context.Context, principal domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func principalFrom(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(domain.Principal)
	return principal, ok
}
