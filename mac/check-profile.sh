#!/bin/sh
# check-profile.sh fails unless a provisioning profile is a Mac App Store distribution profile
# for this app on the expected team, so a development, Developer ID, iOS, or other team's
# profile never reaches a store build.
# Usage: check-profile.sh PROFILE TEAM_ID BUNDLE_ID
set -u

profile=$1
team=$2
bundle=$3

if [ -z "$profile" ] || [ ! -f "$profile" ]; then
	echo "check-profile: PROFILE=<path to the Mac App Store .provisionprofile> is required" >&2
	exit 1
fi

decoded=$(mktemp)
trap 'rm -f "$decoded"' EXIT
if ! security cms -D -i "$profile" >"$decoded" 2>/dev/null; then
	echo "check-profile: $profile is not a signed provisioning profile" >&2
	exit 1
fi

# value prints one key of the decoded profile, or nothing when it is absent.
value() {
	plutil -extract "$1" raw -o - "$decoded" 2>/dev/null
}

failed=0

# fail reports one problem and lets the rest run, so one profile shows everything wrong with it.
fail() {
	echo "check-profile: $1" >&2
	failed=1
}

[ "$(value Platform.0)" = "OSX" ] || fail "profile is not for macOS"
[ "$(value TeamIdentifier.0)" = "$team" ] || fail "profile team is '$(value TeamIdentifier.0)', want $team"
appid=$(value 'Entitlements.com\.apple\.application-identifier')
[ "$appid" = "$team.$bundle" ] || fail "profile app id is '$appid', want $team.$bundle"
plutil -extract ProvisionedDevices raw -o - "$decoded" >/dev/null 2>&1 &&
	fail "profile lists devices, so it is a development profile"
[ "$(value ProvisionsAllDevices)" = "true" ] && fail "profile provisions all devices, so it is not a store profile"
expires=$(value ExpirationDate)
[ -n "$expires" ] && [ "$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$expires" +%s 2>/dev/null || echo 0)" -gt "$(date -u +%s)" ] ||
	fail "profile expired at ${expires:-an unknown date}"

[ "$failed" -eq 0 ] && echo "check-profile: ok, $(value Name), expires $expires"
exit "$failed"
