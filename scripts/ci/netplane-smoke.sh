#!/bin/sh
# Checks a built Netplane image: its version, that it refuses to listen without a token, that it serves the app and
# the API with cmd/netplane/netplane.example.yaml as its config, and that it starts again on the same data volume.
# Run it from the repository root. The example's targets are unreachable documentation addresses: their polls fail,
# which this does not check.
#   netplane-smoke.sh IMAGE VERSION   (ENGINE=docker to use docker instead of podman)
set -eu

image=${1:?image}
version=${2:?version}
engine=${ENGINE:-podman}
token=smoke-$$-token
volume=netplane-smoke-$$
name=netplane-smoke-$$

fail() {
	echo "netplane-smoke: $*" >&2
	"$engine" logs "$name" >&2 2>&1 || true
	exit 1
}
cleanup() {
	"$engine" rm -f "$name" >/dev/null 2>&1 || true
	"$engine" volume rm -f "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT

label=$("$engine" image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image")
[ "$label" = "$version" ] || fail "version label is '$label', not '$version'"
out=$("$engine" run --rm --entrypoint /usr/bin/netplane "$image" version)
[ "$out" = "netplane $version" ] || fail "netplane version prints '$out', not 'netplane $version'"
user=$("$engine" image inspect --format '{{.Config.User}}' "$image")
case $user in '' | root | 0 | 0:*) fail "the image runs as root ('$user')" ;; esac

# Every secret the example names, with a dummy value: a missing one fails the config.
set --
# shellcheck disable=SC2016 # literal ${NAME} references, not expansions
for s in $(grep -o '\${[A-Z0-9_]*}' cmd/netplane/netplane.example.yaml | tr -d '${}' | sort -u); do
	[ "$s" = NETPLANE_API_TOKEN ] || set -- "$@" -e "$s=dummy"
done

# Without a token, serve must refuse to start, not serve an open API on its non-loopback listen.
if out=$(timeout 60 "$engine" run --rm "$@" -v "$PWD/cmd/netplane/netplane.example.yaml:/config/netplane.yaml:ro" \
	"$image" 2>&1); then
	fail "it started without a token"
fi
case $out in *NETPLANE_API_TOKEN*) ;; *) fail "without a token, it failed for another reason: $out" ;; esac

start() {
	"$engine" rm -f "$name" >/dev/null 2>&1 || true
	"$engine" run -d --name "$name" -p 127.0.0.1::8099 -e NETPLANE_API_TOKEN="$token" "$@" \
		-v "$PWD/cmd/netplane/netplane.example.yaml:/config/netplane.yaml:ro" -v "$volume:/data" "$image" >/dev/null
	port=$("$engine" port "$name" 8099 | head -n1 | sed 's/.*://')
	base=http://127.0.0.1:$port
	i=0
	until curl -fsS -o /dev/null "$base/api/v1/health" 2>/dev/null; do
		i=$((i + 1))
		[ "$i" -lt 30 ] || fail "not answering on $base after 30 s"
		[ "$("$engine" inspect --format '{{.State.Running}}' "$name")" = true ] || fail "it exited"
		sleep 1
	done
}
check() {
	curl -fsS "$base/" | grep -q '<title>Andaru Netplane</title>' || fail "GET / does not serve the app"
	code=$(curl -sS -o /dev/null -w '%{http_code}' "$base/api/v1/state")
	[ "$code" = 401 ] || fail "GET /api/v1/state without the token answers $code, not 401"
	curl -fsS -H "Authorization: Bearer $token" "$base/api/v1/state" | grep -q '^{' ||
		fail "GET /api/v1/state with the token does not answer JSON"
}

start "$@"
check
"$engine" stop "$name" >/dev/null
# A new container on the same volume: the database the first one wrote must open again.
start "$@"
check
echo "netplane-smoke: $image $version passed"
