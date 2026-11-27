<p align="center">
  <img src="assets/andaru-icon.svg" alt="Andaru" width="192">
</p>

# andaru
Andaru — a Free Software firewall platform, shipped as its own Fedora CoreOS–based image

## Development

The root-local engine API and CLI expose observed interfaces and durable,
version-checked saved network configuration, preview the systemd-networkd and
pppd files a saved version would produce, and apply the network area behind a
rollback timer that needs explicit confirmation. Saving and previewing never
activate networking.
See [docs/dev/core-api.md](docs/dev/core-api.md) for commands, installation,
security boundaries, and current limits.

## Design research

- [Extension contracts: reuse, avoid, unresolved](docs/research/extension-contract.md)
- [Web UI foundation: technology and interaction direction](docs/research/web-ui-foundation.md)
