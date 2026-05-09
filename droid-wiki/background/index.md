# Background

## Design decisions

### Why a two-process architecture?

The tray process (Windows GUI) and proxy process (console) are separated because Windows GUI applications can't have a visible console without flashing a window. The tray process runs as a hidden GUI app (`-H windowsgui`), while the proxy is spawned as a hidden console process. This separation also allows the proxy to be independently restarted without restarting the tray UI.

### Why protocol translation instead of a unified format?

Different AI tools speak different API formats (Anthropic, OpenAI, Ollama). Rather than forcing clients to adapt to a single format, Prism accepts all three and translates on the fly. This lets users keep their existing tool configurations and just change the base URL.

### Why no sub-packages?

The entire codebase is a single Go package (package `main`). This is intentional — the project is small enough that sub-packages would add complexity without benefit. Files are organized by function with clear naming conventions.

### Why fixed OAuth callback port?

The Codex OAuth flow uses a fixed callback port (1455) instead of a dynamic port. OpenAI's OAuth registration requires a pre-registered redirect URI, making dynamic ports impractical.

## Pitfalls to avoid

### Model name mismatches

If a client sends a model name that isn't in the known models list and doesn't match an alias, Prism falls back to the default model. This can cause unexpected model usage. Always check the proxy logs for `[map] Model remap (default):` messages.

### API key in environment

Prism falls back to environment variables (`OLLAMA_API_KEY`, `OPENCODE_GO_API_KEY`) when API keys are empty in the config file. If you set an API key via the admin UI and it appears to not stick, check whether an environment variable is overriding it.

### Log file growth

Logs accumulate in `%APPDATA%\prism\proxy.log` indefinitely. There's no log rotation. Over long-running sessions, the log file can grow large.
