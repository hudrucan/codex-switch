#!/bin/bash
set -euo pipefail
umask 022

package_root="$(cd "$(dirname "$0")/.." && pwd)"
if [[ $# -ne 2 || "$1" != /* || "$2" != /* ]]; then
    echo 'Usage: build-app.sh /absolute/path/to/codex-switch /absolute/new-output-directory' >&2
    exit 1
fi
cli_binary="$1"
output_dir="$2"
if [[ ! -f "$cli_binary" || ! -x "$cli_binary" || -e "$output_dir" || -L "$output_dir" ]]; then
    echo 'Provide an executable CLI and a new output directory. Nothing is installed.' >&2
    exit 1
fi

scratch_path="${CODEX_SWITCH_BUILD_ROOT:-$package_root/.build}"
swift build --package-path "$package_root" --scratch-path "$scratch_path" -c release --product CodexSwitch
binary_dir="$(swift build --package-path "$package_root" --scratch-path "$scratch_path" -c release --show-bin-path)"
mkdir "$output_dir"
mkdir -p "$output_dir/Codex Switch.app/Contents/MacOS" "$output_dir/Codex Switch.app/Contents/Helpers" "$output_dir/Codex Switch.app/Contents/Resources"
cp "$package_root/Configuration/Info.plist" "$output_dir/Codex Switch.app/Contents/Info.plist"
cp "$package_root/Resources/AppIcon.icns" "$output_dir/Codex Switch.app/Contents/Resources/AppIcon.icns"
cp "$binary_dir/CodexSwitch" "$output_dir/Codex Switch.app/Contents/MacOS/CodexSwitch"
cp "$cli_binary" "$output_dir/Codex Switch.app/Contents/Helpers/codex-switch"
chmod 755 "$output_dir/Codex Switch.app/Contents/MacOS/CodexSwitch" "$output_dir/Codex Switch.app/Contents/Helpers/codex-switch"
plutil -lint "$output_dir/Codex Switch.app/Contents/Info.plist"
echo 'Built Codex Switch.app. No signing, installation, or launch was performed.'
