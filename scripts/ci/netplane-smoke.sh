#!/bin/sh
# Checks a built Netplane image: its version, that it refuses to listen without a token, that it serves the app and
# the API with cmd/netplane/netplane.example.yaml as its config (made for a container, as the README makes it: no
# trust in Home Assistant's ingress proxy), and that a new container finds the first one's database on the volume.
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
work=$(mktemp -d)

fail() {
	echo "netplane-smoke: $*" >&2
	"$engine" logs "$name" >&2 2>&1 || true
	exit 1
}
cleanup() {
	"$engine" rm -fv "$name" >/dev/null 2>&1 || true
	"$engine" volume rm -f "$volume" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

label=$("$engine" image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image")
[ "$label" = "$version" ] || fail "version label is '$label', not '$version'"
out=$("$engine" run --rm --entrypoint /usr/bin/netplane "$image" version)
[ "$out" = "netplane $version" ] || fail "netplane version prints '$out', not 'netplane $version'"
user=$("$engine" image inspect --format '{{.Config.User}}' "$image")
case $user in '' | root | 0 | 0:*) fail "the image runs as root ('$user')" ;; esac

# The example as the README makes it for a container, and one without the token's secret.
sed 's/trust_ingress_proxy: true/trust_ingress_proxy: false/' cmd/netplane/netplane.example.yaml >"$work/netplane.yaml"
grep -q 'trust_ingress_proxy: false' "$work/netplane.yaml" || fail "the README's sed no longer turns trust_ingress_proxy off"
grep -v 'api_token_secret' "$work/netplane.yaml" >"$work/no-token.yaml"
chmod 0644 "$work/netplane.yaml" "$work/no-token.yaml"

# Every secret the example names, with a dummy value: a missing one fails the config.
set --
# shellcheck disable=SC2016 # literal ${NAME} references, not expansions
for s in $(grep -o '\${[A-Z0-9_]*}' cmd/netplane/netplane.example.yaml | tr -d '${}' | sort -u); do
	[ "$s" = NETPLANE_API_TOKEN ] || set -- "$@" -e "$s=dummy"
done

# With no token configured, serve must refuse to start, not serve an open API on its non-loopback listen.
# A server that starts runs on until timeout stops the client (124); the name lets cleanup remove it.
out=$(timeout 60 "$engine" run --rm --name "$name" -v "$work/no-token.yaml:/config/netplane.yaml:ro" "$@" "$image" 2>&1) &&
	rc=0 || rc=$?
[ "$rc" -ne 0 ] && [ "$rc" -ne 124 ] || fail "it started without a token"
case $out in *"refusing to serve unauthenticated"*) ;; *) fail "without a token, it failed for another reason: $out" ;; esac

start() {
	"$engine" rm -fv "$name" >/dev/null 2>&1 || true
	"$engine" run -d --name "$name" -p 127.0.0.1::8099 -e NETPLANE_API_TOKEN="$token" "$@" \
		-v "$work/netplane.yaml:/config/netplane.yaml:ro" -v "$volume:/data" "$image" >/dev/null
	port=$("$engine" port "$name" 8099 2>/dev/null | head -n1 | sed 's/.*://')
	[ -n "$port" ] || fail "no published port: it exited at once"
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
	curl -fsS "$base/" | grep -q '<title>[^<]*Netplane</title>' || fail "GET / does not serve the app"
	code=$(curl -sS -o /dev/null -w '%{http_code}' "$base/api/v1/state")
	[ "$code" = 401 ] || fail "GET /api/v1/state without the token answers $code, not 401"
	curl -fsS -H "Authorization: Bearer $token" "$base/api/v1/state" | grep -q '^{' ||
		fail "GET /api/v1/state with the token does not answer JSON"
}

start "$@"
check
"$engine" stop "$name" >/dev/null
# The database is on the volume, not in the container: it is there to copy out once the container stops.
"$engine" cp "$name:/data/netplane.db" "$work/netplane.db" && [ -s "$work/netplane.db" ] || fail "no database on /data"
# A new container on the same volume: the database the first one wrote must open again.
start "$@"
check
echo "netplane-smoke: $image $version passed"
