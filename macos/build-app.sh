#!/bin/sh
# Builds build/Splitroute.app: the Swift menu bar app with the Go daemon
# (universal binaries) in Contents/Resources. Ad-hoc signed; not notarized.
set -eu
cd "$(dirname "$0")"
VERSION=${VERSION:-dev}
APP=build/Splitroute.app

swift build -c release --arch arm64 --arch x86_64
BIN=$(swift build -c release --arch arm64 --arch x86_64 --show-bin-path)
rm -rf build && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
# SwiftPM records the deployment target as the SDK version, so AppKit would run
# the app in compatibility mode (no Liquid Glass on macOS 26+). Record the real SDK.
vtool -set-build-version macos 13.0 "$(xcrun --show-sdk-version)" -replace \
  -output "$APP/Contents/MacOS/Splitroute" "$BIN/Splitroute"
sed "s/@VERSION@/$VERSION/g" Info.plist > "$APP/Contents/Info.plist"
swift assets/icon.swift "$APP/Contents/Resources/AppIcon.icns"

for arch in arm64 amd64; do
  (cd .. && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "macos/build/splitroute-$arch" .)
done
lipo -create build/splitroute-arm64 build/splitroute-amd64 -output "$APP/Contents/Resources/splitroute"
rm build/splitroute-arm64 build/splitroute-amd64

codesign --force --sign - "$APP/Contents/Resources/splitroute"
codesign --force --sign - "$APP"

# Drag-to-install disk image with a styled window (assets/). dmgbuild writes the
# Finder layout without driving Finder, so it works headless on CI.
VENV=.build/dmgbuild-venv
[ -x "$VENV/bin/dmgbuild" ] || { python3 -m venv "$VENV" && "$VENV/bin/pip" install -q dmgbuild==1.6.7; }
swift assets/background.swift build/background.tiff
"$VENV/bin/dmgbuild" -s assets/dmg-settings.py -D app="$APP" -D background=build/background.tiff \
  Splitroute build/Splitroute.dmg >/dev/null
rm build/background.tiff
echo "$APP build/Splitroute.dmg"
