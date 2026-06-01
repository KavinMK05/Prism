Build command (Windows): go-winres make; go build -ldflags="-H windowsgui" -o prism.exe .
Build command (macOS): CGO_ENABLED=1 go build -o prism .