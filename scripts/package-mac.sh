#!/usr/bin/env bash
# Gói binary đã build thành PortManager.app (bundle tối giản, ad-hoc signed).
set -euo pipefail
cd "$(dirname "$0")/.."

VER=$(sed -n '1p' VERSION | tr -d '[:space:]')
APP=build/PortManager.app

[ -f build/port-manager ] || { echo "Thiếu build/port-manager — chạy make build"; exit 1; }

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"
cp build/port-manager "$APP/Contents/MacOS/port-manager"

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>PortManager</string>
    <key>CFBundleDisplayName</key><string>SMATEC Port Manager</string>
    <key>CFBundleIdentifier</key><string>vn.smatec.port-manager</string>
    <key>CFBundleExecutable</key><string>port-manager</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>${VER}</string>
    <key>CFBundleVersion</key><string>${VER}</string>
    <key>LSMinimumSystemVersion</key><string>12.0</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSHumanReadableCopyright</key><string>Công cụ nội bộ — workspace smatec</string>
</dict>
</plist>
EOF

echo "APPL????" > "$APP/Contents/PkgInfo"
codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || true
echo "Đã gói $APP (v${VER})"
