# Netplane container image

Standalone Netplane, the network view of Andaru without its firewall, as a container image:
`ghcr.io/andaru-project/netplane`. Each Andaru release publishes it for amd64 and arm64, tagged with the version and,
for a release that is not a prerelease, `latest`. The release also carries a static `netplane` binary per
architecture (`netplane-<version>-linux-amd64` and `-linux-arm64`), listed in its signed `SHA256SUMS`.

## Run

```sh
cp cmd/netplane/netplane.example.yaml netplane.yaml   # then edit its targets
openssl rand -hex 32 >token                           # the API token
docker run -d --name netplane -p 8099:8099 \
  -v "$PWD/netplane.yaml:/config/netplane.yaml:ro" \
  -v netplane-data:/data \
  -e NETPLANE_API_TOKEN="$(cat token)" \
  --env-file secrets.env \
  ghcr.io/andaru-project/netplane:latest
```

Then open `http://<host>:8099/` and enter the token. `podman` takes the same arguments.

- **`/config`** holds `netplane.yaml`, read once at start; mount it read-only. The image runs
  `netplane serve --config /config/netplane.yaml --db /data/netplane.db --listen :8099`, so these flags override the
  file's `server.listen` and `server.db_path`. Arguments after the image name replace all three flags.
- **`/data`** holds the SQLite database: keep it on a volume, or every restart starts from an empty history. A named
  volume is writable as created. A bind-mounted directory must be writable by the image's user, UID and GID 65532
  (`chown 65532:65532 <dir>`).
- **The token** is required: the server listens on all addresses, and without a token it refuses to start. Pass it
  as `NETPLANE_API_TOKEN`. The example config names it with `api_token_secret: "${NETPLANE_API_TOKEN}"`; point that
  at another variable to read the token from there. Clients send it as `Authorization: Bearer <token>`.
- **Target secrets** are environment variables too: each `secrets` entry of the config names one (`${PFSENSE_API_KEY}`).
  An enabled target whose variable is unset fails the start. An `--env-file` keeps them out of the shell history.
- **`trust_ingress_proxy`** lets requests from Home Assistant's ingress proxy (172.30.32.2) in without the token. The
  example sets it for the add-on; set it to `false` when Netplane does not run under Home Assistant.
- Hosts other than IP addresses and private names (`.lan`, `.home.arpa`, ...) need `allowed_hosts`, and a reverse
  proxy that rewrites `Host` needs `trusted_origins`: see the comments in the example config.

## Verify

The image is signed with Andaru's release key:

```sh
cosign verify --key image/cosign.pub ghcr.io/andaru-project/netplane:<version>
```

## Build and check from source

From the repository root:

```sh
podman build -f packaging/netplane/Containerfile --build-arg VERSION=0.0.0-dev -t netplane:dev .
sh scripts/ci/netplane-smoke.sh netplane:dev 0.0.0-dev   # ENGINE=docker to use docker
```

The smoke test runs the image with the example config and a token, fetches the app and `/api/v1/state`, checks that
it refuses to start without a token, and starts it again on the same data volume. CI runs it on every pull request.
