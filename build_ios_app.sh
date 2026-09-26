#!/bin/bash
# Full OpenFlux iOS pipeline: build Go lib -> generate Xcode project ->
# archive -> export App Store IPA -> (optional) upload to TestFlight.
#
# Requirements: Xcode, xcodegen (brew install xcodegen), Go 1.26+.
# Signing: automatic; your Apple ID must be logged into Xcode
# (Xcode > Settings > Accounts) and belong to team 8GQH8GQ252.
set -e

ROOT="$(cd "$(dirname "$0")" && pwd)"
APP_DIR="$ROOT/ios-app"
TEAM_ID="8GQH8GQ252"

echo "==> [1/5] Building Go static library (arm64, iOS)"
"$ROOT/build_ios.sh"

echo "==> [2/5] Syncing library into the app"
mkdir -p "$APP_DIR/Lib"
  cp "$ROOT/output/ios/liboflux.a" "$APP_DIR/Lib/liboflux.a"
cp "$ROOT/output/ios/liboflux.h" "$APP_DIR/Lib/liboflux.h"

echo "==> [3/5] Generating Xcode project"
cd "$APP_DIR"
xcodegen generate

# App Store Connect API key, if present. Without it xcodebuild needs an Apple ID
# signed into Xcode's Accounts, and signing here is cloud-managed: no account
# means no distribution identity and the export dies with "No Accounts". The key
# is the CI-sanctioned way in and keeps the pipeline working headless.
# Key id comes from the file name; the issuer from $ASC_ISSUER_ID or
# ~/.appstoreconnect/issuer_id. Neither is a secret — the .p8 is, and it stays
# outside the repo.
ASC_ARGS=()
ASC_KEY=$(ls "$HOME"/.appstoreconnect/private_keys/AuthKey_*.p8 2>/dev/null | head -1)
ASC_ISSUER="${ASC_ISSUER_ID:-$(cat "$HOME/.appstoreconnect/issuer_id" 2>/dev/null)}"
if [ -n "$ASC_KEY" ] && [ -n "$ASC_ISSUER" ]; then
  ASC_KEY_ID=$(basename "$ASC_KEY" .p8); ASC_KEY_ID=${ASC_KEY_ID#AuthKey_}
  ASC_ARGS=(-authenticationKeyPath "$ASC_KEY" \
            -authenticationKeyID "$ASC_KEY_ID" \
            -authenticationKeyIssuerID "$ASC_ISSUER")
  echo "    (signing via App Store Connect key $ASC_KEY_ID)"
fi

echo "==> [4/5] Archiving (Release)"
rm -rf build/OpenFlux.xcarchive
xcodebuild -project OpenFlux.xcodeproj -scheme OpenFlux -configuration Release \
  -destination 'generic/platform=iOS' \
  -archivePath build/OpenFlux.xcarchive \
  -allowProvisioningUpdates "${ASC_ARGS[@]}" \
  clean archive

echo "==> [5/5] Exporting App Store IPA"
rm -rf build/export
xcodebuild -exportArchive \
  -archivePath build/OpenFlux.xcarchive \
  -exportPath build/export \
  -exportOptionsPlist ExportOptions.plist \
  -allowProvisioningUpdates "${ASC_ARGS[@]}"

echo ""
echo "IPA ready: $APP_DIR/build/export/OpenFlux.ipa"
echo ""
echo "To upload to TestFlight, first create the app record in App Store Connect"
echo "(My Apps > + > New App, bundle id com.p1neapplexpress-saharev.openflux), then run:"
echo ""
echo "  # Option A - app-specific password (appleid.apple.com > App-Specific Passwords):"
echo "  xcrun altool --upload-app -f build/export/OpenFlux.ipa -t ios \\"
echo "    -u YOUR_APPLE_ID -p xxxx-xxxx-xxxx-xxxx"
echo ""
echo "  # Option B - App Store Connect API key (.p8 in ~/.appstoreconnect/private_keys/):"
echo "  xcrun altool --upload-app -f build/export/OpenFlux.ipa -t ios \\"
echo "    --apiKey KEY_ID --apiIssuer ISSUER_ID"
