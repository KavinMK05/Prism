#!/bin/bash
set -e

echo "Building Prism for macOS..."

CGO_ENABLED=1 go build -o prism .

mkdir -p Prism.app/Contents/MacOS
mkdir -p Prism.app/Contents/Resources

cp prism Prism.app/Contents/MacOS/prism
cp logo_icon.icns Prism.app/Contents/Resources/prism.icns 2>/dev/null || true
cp Info.plist Prism.app/Contents/Info.plist

# PkgInfo is required for macOS to recognize this as an app bundle
echo -n "APPL????" > Prism.app/Contents/PkgInfo

# Ensure binary is executable
chmod +x Prism.app/Contents/MacOS/prism

# Create a zip preserving the .app structure for distribution
ditto -c -k --keepParent Prism.app Prism-macOS.zip

echo "Built Prism.app"
echo "Created Prism-macOS.zip for distribution"