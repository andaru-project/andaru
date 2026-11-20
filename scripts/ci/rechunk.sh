#!/bin/sh
# Rebuilds a built Andaru image's layers from its final filesystem, one layer per package. A box then
# downloads only the layers whose packages changed, and nothing the build removed: without this, the base's layers
# still carry every file a later step deletes (moby-engine and containerd among them). Labels and config are kept.
#   rechunk.sh IMAGE   (a freshly built image: rechunking one already rechunked may leave its ID unchanged)
set -eu

image=${1:?image}
# chunkah v0.7.1, the index of both architectures.
chunkah=quay.io/coreos/chunkah@sha256:71e593421c5de7dcc7c8c5c50e98d3bac572055a76f399f61a491d123c20a523

# A file, not CHUNKAH_CONFIG_STR: the image's history makes the inspect output too long for an environment variable.
config=$(mktemp)
trap 'rm -f "$config"' EXIT
podman image inspect "$image" >"$config"
before=$(podman image inspect -f '{{.Id}}' "$image")
# One layer per package, as the Fedora CoreOS base has (about 300 today, 320 with ours); 500 is the storage limit.
# --prune and the ostree labels only matter for an ostree-native base: they describe layers that no longer exist.
podman run --rm --network=none --mount=type=image,src="$image",target=/chunkah -v "$config:/config.json:ro,z" \
	"$chunkah" build --config /config.json --prune /sysroot/ --max-layers 448 \
	--label ostree.commit- --label ostree.final-diffid- -t "$image" |
	podman load
# The pipeline's status is the load's: make sure the tag moved to the new image.
[ "$(podman image inspect -f '{{.Id}}' "$image")" != "$before" ] || {
	echo "rechunk.sh: $image was not replaced (was it already rechunked?)" >&2
	exit 1
}
podman run --rm "$image" bootc container lint --fatal-warnings
