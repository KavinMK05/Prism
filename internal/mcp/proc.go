package mcp

import (
	"os"
	"os/exec"
	"strings"
)

// commandHandle wraps *exec.Cmd so the platform files can share one shape.
type commandHandle struct {
	cmd *exec.Cmd
}

func newCommandHandle(cmd *exec.Cmd) *commandHandle {
	return &commandHandle{cmd: cmd}
}

// Kill terminates the process. The error is ignored: the only reason to call
// this is that the transport is already going away.
func (h *commandHandle) Kill() {
	if h == nil || h.cmd == nil || h.cmd.Process == nil {
		return
	}
	_ = h.cmd.Process.Kill()
	_, _ = h.cmd.Process.Wait()
}

// mergedEnv builds the child environment: the parent's, plus the server's
// configured overrides. Matching is case-insensitive on Windows, where PATH
// and Path are the same variable.
func mergedEnv(extra map[string]string) []string {
	base := os.Environ()
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	overridden := map[string]bool{}
	for k := range extra {
		overridden[strings.ToUpper(k)] = true
	}
	for _, kv := range base {
		idx := strings.IndexByte(kv, '=')
		if idx <= 0 {
			continue
		}
		if overridden[strings.ToUpper(kv[:idx])] {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}
