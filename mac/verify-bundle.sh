#!/bin/sh
# verify-bundle.sh fails unless a built app is the shape a person can install: it runs on Apple
# silicon and Intel, says the version it was built from, carries its category and copyright, and
# names no path on the machine that built it.
# Usage: verify-bundle.sh APP VERSION MINIMUM_MACOS
set -u

app=$1
version=$2
floor=$3
binary="$app/Contents/MacOS/WhoToAsk"
plist="$app/Contents/Info.plist"
failed=0

# fail reports one problem and lets the rest run, so one build shows everything wrong with it.
fail() {
	echo "verify-bundle: $1" >&2
	failed=1
}

# value prints one key of the bundle's Info.plist, or nothing when it is absent.
value() {
	plutil -extract "$1" raw -o - "$plist" 2>/dev/null
}

for arch in arm64 x86_64; do
	lipo -archs "$binary" | tr ' ' '\n' | grep -qx "$arch" || fail "$binary has no $arch slice"
	minos=$(vtool -arch "$arch" -show-build "$binary" 2>/dev/null | awk '$1 == "minos" {print $2}')
	[ "$minos" = "$floor" ] || fail "$arch slice runs from macOS ${minos:-unknown}, want $floor"
done

short=$(value CFBundleShortVersionString)
[ -n "$version" ] || fail "no version to compare: is there a v* tag?"
[ "$short" = "$version" ] || fail "CFBundleShortVersionString is '$short', want '$version'"
echo "$short" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' || fail "'$short' is not three numbers"
value CFBundleVersion | grep -Eqx '[0-9]+' || fail "CFBundleVersion is not an integer"
value LSApplicationCategoryType | grep -q '^public\.app-category\.' || fail "no LSApplicationCategoryType"
value NSHumanReadableCopyright | grep -q '^Copyright' || fail "no NSHumanReadableCopyright"
codesign --verify --strict "$app" 2>/dev/null || fail "$app does not verify as signed"

# A shipped binary must not name the machine it was built on: home folder paths leak through Go
# build info in the strings and through Swift debug stabs that only nm shows.
paths=$(strings -a "$binary" | grep -c '/Users/')
[ "$paths" -eq 0 ] || fail "$binary holds $paths build machine paths in its strings"
for arch in arm64 x86_64; do
	stabs=$(nm -pa -arch "$arch" "$binary" 2>/dev/null | grep -c '/Users/')
	[ "$stabs" -eq 0 ] || fail "$arch slice holds $stabs build machine paths in its symbols"
done

[ "$failed" -eq 0 ] && echo "verify-bundle: ok, $short ($(value CFBundleVersion)), $(lipo -archs "$binary")"
exit "$failed"
