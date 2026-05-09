# How to contribute

## Getting started

To contribute to Prism, you'll need:

- Go 1.26.2 or later
- `go-winres` for building with Windows resources
- Windows 10+ (the project targets Windows only)

Build command:

```powershell
go-winres make; go build -ldflags="-H windowsgui" -o prism.exe .
```

For a console-mode debug build (without system tray):

```powershell
go build -o prism.exe .
```

## Codebase navigation

All source files are in the root package. See the [patterns and conventions](patterns-and-conventions.md) page for file organization and coding style.

## PR process

1. Fork the repository and create a feature branch
2. Make your changes, following the existing patterns
3. Test with the verification commands in the [Getting Started guide](../overview/getting-started.md)
4. Submit a PR against the `main` branch
