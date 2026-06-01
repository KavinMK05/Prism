#!/bin/bash
set -e

echo "Building Prism for macOS..."

CGO_ENABLED=1 go build -o prism .

mkdir -p Prism.app/Contents/MacOS
mkdir -p Prism.app/Contents/Resources

cp prism Prism.app/Contents/MacOS/prism
cp logo_icon.icns Prism.app/Contents/Resources/prism.icns 2>/dev/null || true
cp Info.plist Prism.app/Contents/Info.plist

echo "Built Prism.app"