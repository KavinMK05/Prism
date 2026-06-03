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

# Code signing
SIGNING_IDENTITY="${APPLE_SIGNING_IDENTITY:-}"
if [ -n "$SIGNING_IDENTITY" ]; then
    echo "Signing with Developer ID: $SIGNING_IDENTITY"
    codesign --force --deep --options runtime --entitlements Prism.entitlements --sign "$SIGNING_IDENTITY" Prism.app
else
    echo "No signing identity set, performing ad-hoc signing"
    codesign --force --deep -s - Prism.app
fi

# Create DMG for distribution
echo "Creating DMG..."
mkdir -p dmg_temp
cp -R Prism.app dmg_temp/
ln -s /Applications dmg_temp/Applications
hdiutil create -volname "Prism" -srcfolder dmg_temp -ov -format UDZO Prism-macOS.dmg
rm -rf dmg_temp

# Notarize if credentials are available
if [ -n "$APPLE_ID" ] && [ -n "$APPLE_APP_PASSWORD" ] && [ -n "$APPLE_TEAM_ID" ]; then
    echo "Notarizing Prism-macOS.dmg..."
    xcrun notarytool submit Prism-macOS.dmg \
        --apple-id "$APPLE_ID" \
        --password "$APPLE_APP_PASSWORD" \
        --team-id "$APPLE_TEAM_ID" \
        --wait
    echo "Stapling notarization ticket..."
    xcrun stapler staple Prism-macOS.dmg
    echo "Notarization complete!"
else
    echo "Skipping notarization (APPLE_ID / APPLE_APP_PASSWORD / APPLE_TEAM_ID not set)"
fi

echo "Built Prism.app"
echo "Created Prism-macOS.dmg for distribution"
