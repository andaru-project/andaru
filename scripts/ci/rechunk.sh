#!/bin/sh
# Rebuilds a built Andaru image's layers from its final filesystem, one layer per group of packages. A box then
# downloads only the layers whose packages changed, and nothing the build removed: without this, the base's layers
# still carry every file a later step deletes (moby-engine and containerd among them). Labels and config are kept.
#   rechunk.sh IMAGE
set -eu

image=${1:?image}
# chunkah v0.7.1, the index of both architectures.
chunkah=quay.io/coreos/chunkah@sha256:71e593421c5de7dcc7c8c5c50e98d3bac572055a76f399f61a491d123c20a523

# A file, not CHUNKAH_CONFIG_STR: the image's history makes the inspect output too long for an environment variable.
config=$(mktemp)
trap 'rm -f "$config"' EXIT
podman image inspect "$image" >"$config"
# --prune /sysroot/ drops the base's ostree repository, which bootc does not need in an image. The ostree labels
# describe the base's layers, which no longer exist.
podman run --rm --mount=type=image,src="$image",target=/chunkah -v "$config:/config.json:ro" "$chunkah" \
	build --config /config.json --prune /sysroot/ --max-layers 128 \
	--label ostree.commit- --label ostree.final-diffid- -t "$image" |
	podman load
podman run --rm "$image" bootc container lint --fatal-warnings
