#!/bin/bash
set -e

echo "Building Prism for macOS..."

CGO_ENABLED=1 go build -o prism .

# Generate .icns from icon.png
echo "Generating .icns icon..."
mkdir -p logo_icon.iconset
sips -z 16 16     icon.png --out logo_icon.iconset/icon_16x16.png
sips -z 32 32     icon.png --out logo_icon.iconset/icon_16x16@2x.png
sips -z 32 32     icon.png --out logo_icon.iconset/icon_32x32.png
sips -z 64 64     icon.png --out logo_icon.iconset/icon_32x32@2x.png
sips -z 128 128   icon.png --out logo_icon.iconset/icon_128x128.png
sips -z 256 256   icon.png --out logo_icon.iconset/icon_128x128@2x.png
sips -z 256 256   icon.png --out logo_icon.iconset/icon_256x256.png
sips -z 512 512   icon.png --out logo_icon.iconset/icon_256x256@2x.png
sips -z 512 512   icon.png --out logo_icon.iconset/icon_512x512.png
sips -z 1024 1024 icon.png --out logo_icon.iconset/icon_512x512@2x.png
iconutil -c icns logo_icon.iconset -o logo_icon.icns
rm -rf logo_icon.iconset

mkdir -p Prism.app/Contents/MacOS
mkdir -p Prism.app/Contents/Resources

cp prism Prism.app/Contents/MacOS/prism
cp logo_icon.icns Prism.app/Contents/Resources/prism.icns
cp Info.plist Prism.app/Contents/Info.plist

# PkgInfo is required for macOS to recognize this as an app bundle
echo -n "APPL????" > Prism.app/Contents/PkgInfo

# Ensure binary is executable
chmod +x Prism.app/Contents/MacOS/prism

# Ad-hoc codesign to bypass Gatekeeper damaged warning
codesign --force --deep -s - Prism.app

# Create DMG for distribution
echo "Creating DMG..."
mkdir -p dmg_temp
cp -R Prism.app dmg_temp/
ln -s /Applications dmg_temp/Applications
hdiutil create -volname "Prism" -srcfolder dmg_temp -ov -format UDZO Prism-macOS.dmg
rm -rf dmg_temp

echo "Built Prism.app"
echo "Created Prism-macOS.dmg for distribution"