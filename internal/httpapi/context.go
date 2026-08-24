package httpapi

import (
	"context"

	"github.com/vance1852/estate-financial-claim-service/internal/domain"
)

type principalKey struct{}

var principalSlots = func() chan *domain.Principal {
	slots := make(chan *domain.Principal, 1)
	slots <- &domain.Principal{}
	return slots
}()

func acquirePrincipal(principal domain.Principal) *domain.Principal {
	var slot *domain.Principal
	select {
	case slot = <-principalSlots:
	default:
		slot = &domain.Principal{}
	}
	*slot = principal
	return slot
}

func releasePrincipal(principal *domain.Principal) {
	select {
	case principalSlots <- principal:
	default:
	}
}

func withPrincipal(ctx context.Context, principal *domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func principalFrom(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(*domain.Principal)
	if !ok || principal == nil {
		return domain.Principal{}, false
	}
	return *principal, true
}
