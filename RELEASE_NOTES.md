# Prism v0.3.5

## Bug Fixes

- **Fixed: SearXNG failed to start with "Address already in use" (port 8888) after an unclean Prism exit.** When Prism was force-quit or crashed without reaping its child SearXNG process, the orphaned webapp survived and kept holding port 8888. On the next launch Prism saw no tracked SearXNG PID (`isSearxngRunning()` false), spawned a fresh webapp, which couldn't bind 8888 and crashed — and the crash-restart loop gave up after 5 attempts in 60s, leaving SearXNG permanently down. The existing `killOrphanOnPort()` only reclaimed the proxy port (11434); SearXNG's port was never reclaimed. Prism now reclaims the configured SearXNG port before spawning the webapp, killing any orphan holding it while sparing Prism's own tracked PID. The recursive restart path also flows through this, so crash-restarts benefit too.

- **Refactor: shared `killOrphansOnPort(port, knownPID)` helper (macOS + Windows).** Extracted from the proxy-only `killOrphanOnPort()` so both the proxy and SearXNG reclaim ports the same way. Added `port_orphan_test.go` verifying both branches: a known PID is spared (returns 0, process alive) and a foreign orphan is killed (returns ≥1, process exits).

## Notes

- The SearXNG log lines `fatal: not a git repository`, `missing config file ... limiter.toml`, and `ahmia`/`torch: can't register engine` are harmless SearXNG-internal noise (running from an extracted tarball; limiter is off; optional engines missing optional deps) and do not affect search.