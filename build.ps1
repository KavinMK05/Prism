# Build script for Windows
# Builds the React frontend, then the Go binary
cd web
npm run build
cd ..
go-winres make
go build -ldflags="-H windowsgui -X main.version=dev" -o prism.exe .
