package audit

import (
	"context"
	"runtime"
	"sync"

	"github.com/google/uuid"
)

// Actor describes who performed a data change and from where.
type Actor struct {
	UserID    uuid.UUID // authenticated user who performed the action
	UserName  string
	ScopeID   uuid.UUID // data-scope owner (store owner) used for tenant filtering
	IPAddress string
	UserAgent string
	RequestID string
}

type actorContextKey struct{}

// ContextWithActor attaches an actor to a context. Database operations executed
// with db.WithContext(ctx) will resolve their actor from this context.
func ContextWithActor(ctx context.Context, a *Actor) context.Context {
	return context.WithValue(ctx, actorContextKey{}, a)
}

func actorFromContext(ctx context.Context) *Actor {
	if ctx == nil {
		return nil
	}
	if a, ok := ctx.Value(actorContextKey{}).(*Actor); ok {
		return a
	}
	return nil
}

// goroutine-bound actor registry. Auth middleware binds the authenticated actor
// to the request goroutine so GORM callbacks can resolve who triggered a write
// without every call site threading the request context through.
var actorRegistry sync.Map // int64 -> *Actor

// BindActor registers the actor for the current goroutine and returns an
// unbind function that must be called when the request finishes.
func BindActor(a *Actor) func() {
	id := curGID()
	actorRegistry.Store(id, a)
	return func() { actorRegistry.Delete(id) }
}

func currentGoroutineActor() *Actor {
	if a, ok := actorRegistry.Load(curGID()); ok {
		return a.(*Actor)
	}
	return nil
}

// resolveActor prefers an explicit context-bound actor, then the
// goroutine-bound actor set by the auth middleware.
func resolveActor(ctx context.Context) *Actor {
	if a := actorFromContext(ctx); a != nil {
		return a
	}
	return currentGoroutineActor()
}

// curGID parses the current goroutine id from runtime.Stack. This is the only
// supported way to correlate GORM callbacks (which receive no gin context) with
// the HTTP request that triggered them.
func curGID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// frame header: "goroutine 1234 ["
	b := buf[:n]
	var id int64
	for i := len("goroutine "); i < len(b) && b[i] >= '0' && b[i] <= '9'; i++ {
		id = id*10 + int64(b[i]-'0')
	}
	return id
}
