#!/bin/bash
# Build script for macOS
# Builds the React frontend, then the Go binary
set -e
cd web
npm run build
cd ..
CGO_ENABLED=1 go build -ldflags="-X main.version=dev" -o prism .
