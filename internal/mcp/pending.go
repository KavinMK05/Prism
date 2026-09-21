package mcp

import (
	"context"
	"encoding/json"
	"sync"
)

// pending tracks in-flight JSON-RPC requests by id for transports that receive
// responses asynchronously (stdio line framing and the legacy HTTP+SSE
// transport, where replies arrive on an open stream rather than in the body of
// the POST that carried the request).
type pending struct {
	mu sync.Mutex
	m  map[string]chan *rpcResponse
}

func newPending() *pending {
	return &pending{m: map[string]chan *rpcResponse{}}
}

// add registers a waiter for one request id. The returned func removes the
// entry and must always be called (defer) once the request completes.
func (p *pending) add(id json.RawMessage) (chan *rpcResponse, func()) {
	key := string(id)
	ch := make(chan *rpcResponse, 1)
	p.mu.Lock()
	p.m[key] = ch
	p.mu.Unlock()
	return ch, func() {
		p.mu.Lock()
		delete(p.m, key)
		p.mu.Unlock()
	}
}

// deliver routes a response to its waiter. Returns false when nobody is
// waiting (a late reply to a timed-out request, or an id Prism never sent).
func (p *pending) deliver(resp *rpcResponse) bool {
	if resp == nil || len(resp.ID) == 0 {
		return false
	}
	key := string(resp.ID)
	p.mu.Lock()
	ch, ok := p.m[key]
	p.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- resp:
	default:
	}
	return true
}

// clear drops every waiter, used when a transport dies so blocked callers
// fail fast instead of waiting for their own timeout.
func (p *pending) clear() {
	p.mu.Lock()
	p.m = map[string]chan *rpcResponse{}
	p.mu.Unlock()
}

// awaitResponse blocks until the transport delivers a reply or ctx expires.
func awaitResponse(ctx context.Context, ch chan *rpcResponse) (*rpcResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		return resp, nil
	}
}

// responseError converts a JSON-RPC response into a Go error, preferring the
// protocol error so callers can inspect the code (e.g. -32601).
func responseError(resp *rpcResponse) error {
	if resp == nil {
		return rpcErrorf(CodeInternalError, "empty response")
	}
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}
