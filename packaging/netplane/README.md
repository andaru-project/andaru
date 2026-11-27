# Netplane container image

Standalone Netplane, the network view of Andaru without its firewall, as a container image:
`ghcr.io/andaru-project/netplane`. Each Andaru release publishes it for amd64 and arm64, tagged with the version and,
for a release that is not a prerelease, `latest`. The release also carries a static `netplane` binary per
architecture (`netplane-<version>-linux-amd64` and `-linux-arm64`), listed in its signed `SHA256SUMS`.

## Run

In a directory of its own, outside any repository: it holds the token.

```sh
mkdir -p ~/netplane && cd ~/netplane
# The example, without trust in Home Assistant's ingress proxy (see below); then edit its targets.
sed 's/trust_ingress_proxy: true/trust_ingress_proxy: false/' \
  <path-to-andaru>/cmd/netplane/netplane.example.yaml >netplane.yaml
# The API token, and every secret the targets name, one NAME=value a line.
umask 077
printf 'NETPLANE_API_TOKEN=%s\n' "$(openssl rand -hex 32)" >secrets.env
echo 'PFSENSE_API_KEY=...' >>secrets.env   # one line per ${NAME} in netplane.yaml
docker run -d --name netplane -p 8099:8099 \
  -v "$PWD/netplane.yaml:/config/netplane.yaml:ro" \
  -v netplane-data:/data \
  --env-file secrets.env \
  ghcr.io/andaru-project/netplane:latest
```

Then open `http://<host>:8099/` and sign in with the token from `secrets.env`. `podman` takes the same arguments; on a
host with SELinux enforcing (Fedora, RHEL, CoreOS), mount the config with `:ro,Z` so the container may read it.

- **`/config`** holds `netplane.yaml`, read once at start; mount it read-only. The image runs
  `netplane serve --config /config/netplane.yaml --db /data/netplane.db --listen :8099`, so these flags override the
  file's `server.listen` and `server.db_path`. Arguments after the image name replace all three flags.
- **Removing it:** `docker rm -fv netplane`. The image declares `/config` a volume too, and `-v` removes the empty
  one each run leaves; `netplane-data` stays.
- **`/data`** holds the SQLite database: keep it on a volume, or every restart starts from an empty history. A named
  volume is writable as created. A bind-mounted directory must be writable by the image's user, UID and GID 65532
  (`chown 65532:65532 <dir>`).
- **The token** is required: the server listens on all addresses, and without a token it refuses to start. Pass it
  as `NETPLANE_API_TOKEN`, in the env file rather than with `-e`, which shows it in the process list. The example config names it with `api_token_secret: "${NETPLANE_API_TOKEN}"`; point that
  at another variable to read the token from there. Clients send it as `Authorization: Bearer <token>`.
- **Target secrets** are environment variables too: each `secrets` entry of the config names one (`${PFSENSE_API_KEY}`).
  An enabled target whose variable is unset fails the start. An `--env-file` keeps them out of the shell history.
- **`trust_ingress_proxy`** lets requests from 172.30.32.2, Home Assistant's ingress proxy, in without the token.
  The example sets it for the add-on. Outside Home Assistant it must be `false`, as the steps above make it: any
  other host at that address, a Docker network in 172.30.0.0/16 or a LAN client, would skip the token.
- A reverse proxy that rewrites `Host` needs `trusted_origins`: see the comments in the example config.
  (`allowed_hosts` applies only to a server without a token, which this image never is.)

## Verify

The image is signed with Andaru's release key:

```sh
cosign verify --key image/cosign.pub ghcr.io/andaru-project/netplane:<version>
```

Only `<version>` and `latest` are set after the signature is verified. The per-architecture tags the release pushes
on the way (`<version>-amd64`, `<version>-arm64`, `staging-<version>`) are unsigned until then: pull the version.

## Build and check from source

From the repository root:

```sh
podman build -f packaging/netplane/Containerfile --build-arg VERSION=0.0.0-dev -t netplane:dev .
sh scripts/ci/netplane-smoke.sh netplane:dev 0.0.0-dev   # ENGINE=docker to use docker
```

The smoke test runs the image with the example config, made as above, and a token. It fetches the app and
`/api/v1/state`, checks that it refuses to start with no token configured, that its database is on the volume, and
that a new container starts on that volume. CI runs it on every pull request.
