package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/util"
)

const (
	// autoConnectWait bounds how long the call path blocks for the user to
	// approve. The sign-in window itself lives oauthFlowTTL regardless, so an
	// approval after this still lands; only the current call gives up.
	autoConnectWait = 2 * time.Minute
	// autoConnectCooldown keeps a failure from reopening the browser on every
	// retry the agent makes. Time-based, so a transient discovery failure is
	// retried soon instead of latching the server as permanently un-attempted.
	autoConnectCooldown = 60 * time.Second
)

// errAutoConnectPending reports that the sign-in has not finished; callers keep
// whatever error they already had.
var errAutoConnectPending = errors.New("waiting for the sign-in to be approved")

// openAuthURL opens an authorize URL in the browser; a variable so tests can
// complete a flow without opening anything.
var openAuthURL = util.OpenBrowser

var (
	autoMu     sync.Mutex
	autoFailed = map[string]time.Time{}
)

// autoConnectEligible reports whether an automatic sign-in may start for this
// server: the user marked it OAuth, it has no usable credential, and both the
// process (autoAuthorize wired) and the setting allow it. Deliberately narrow:
// a server that merely 401s has not been vouched for by anyone, and discovery
// follows URLs that server controls, so it must not open a browser on its own.
func (m *Manager) autoConnectEligible(s *config.MCPServerConfig) bool {
	if s == nil || m.autoAuthorize == nil || hasCredentials(s) {
		return false
	}
	if s.AuthMode != config.MCPAuthOAuth || s.Transport == config.MCPTransportStdio || s.URL == "" {
		return false
	}
	cfg := m.cfg()
	return cfg != nil && cfg.MCP.AutoConnectEnabled()
}

// AutoAuthorize signs in for a server whose tools an agent just asked for, and
// waits for the approval when wait is set. wait=false is the tools/list
// behavior: the request returns needs_auth now while the window opens in the
// background, because the per-server budget there is toolListTimeout (20s) and
// no human approves inside it.
func (m *Manager) AutoAuthorize(ctx context.Context, serverID string, wait bool) error {
	cfg := m.cfg()
	if cfg == nil {
		return errAutoConnectPending
	}
	s := cfg.FindMCPServer(serverID)
	if !m.autoConnectEligible(s) {
		return errAutoConnectPending
	}
	if flow := pendingFlowFor(serverID); flow != nil {
		if !wait {
			return errAutoConnectPending // already waiting on the user
		}
		return flow.wait(ctx)
	}
	if !autoConnectAllowed(serverID) {
		return errAutoConnectPending // a recent failure; back off
	}

	// Discovery must not inherit the caller's deadline, which is usually spent.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoConnectWait)
	flow, authURL, err := startOAuthFlow(dctx, cfg, serverID)
	cancel()
	if err != nil {
		markAutoConnectFailed(serverID)
		log.Printf("[MCP] %s: automatic sign-in could not start: %v", serverID, err)
		return errAutoConnectPending
	}
	if err := openAuthURL(authURL); err != nil {
		flow.finishFlow(fmt.Errorf("could not open the browser: %w", err))
		markAutoConnectFailed(serverID)
		log.Printf("[MCP] %s: automatic sign-in could not open the browser: %v", serverID, err)
		return errAutoConnectPending
	}
	log.Printf("[MCP] %s: started an automatic sign-in; the browser is open", serverID)
	if !wait {
		return errAutoConnectPending
	}
	werr := flow.wait(ctx)
	if werr != nil {
		markAutoConnectFailed(serverID)
	}
	return werr
}

// triggerAutoAuthorize invokes the installed auto-sign-in callback. The
// Gateway goes through this indirection rather than calling AutoAuthorize
// directly so the trigger is replaceable in tests and inert when no process
// wired one.
func (m *Manager) triggerAutoAuthorize(ctx context.Context, serverID string, wait bool) error {
	fn := m.autoAuthorize
	if fn == nil {
		return errAutoConnectPending
	}
	return fn(ctx, serverID, wait)
}

// pendingFlowFor returns an unfinished flow for a server, if any.
func pendingFlowFor(serverID string) *oauthFlow {
	flowMu.Lock()
	defer flowMu.Unlock()
	for _, f := range flowsByState {
		if f.serverID != serverID {
			continue
		}
		select {
		case <-f.done:
			continue // already terminal
		default:
			return f
		}
	}
	return nil
}

// autoConnectAllowed reports whether the cooldown since the last failure has
// elapsed; markAutoConnectFailed records one.
func autoConnectAllowed(serverID string) bool {
	autoMu.Lock()
	defer autoMu.Unlock()
	at := autoFailed[serverID]
	return at.IsZero() || time.Since(at) >= autoConnectCooldown
}

func markAutoConnectFailed(serverID string) {
	autoMu.Lock()
	autoFailed[serverID] = time.Now()
	autoMu.Unlock()
}
