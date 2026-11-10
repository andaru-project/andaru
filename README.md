<p align="center">
  <img src="assets/andaru-icon.svg" alt="Andaru" width="192">
</p>

# andaru
Andaru — a Free Software firewall platform for Debian

## Development

The root-local engine API and CLI expose observed interfaces and durable,
version-checked saved network configuration, and preview the systemd-networkd and
pppd files a saved version would produce. Neither saving nor previewing activates
networking.
See [docs/dev/core-api.md](docs/dev/core-api.md) for commands, installation,
security boundaries, and current limits.

A two-VM QEMU lab (Debian 13 arm64, serial console only) is used for repeated,
reproducible testing against the two baselines Andaru must handle. The scripts
are in [`dev/vm-lab/`](dev/vm-lab); see [docs/dev/vm-lab.md](docs/dev/vm-lab.md)
for what it is, why it is serial-only, and how to run it.

```sh
dev/vm-lab/lab-setup
```

AI-assisted work uses Graphify and CodeGraph indexes; run
`sh scripts/setup-hooks.sh` once per clone. See
[docs/dev/agent-tooling.md](docs/dev/agent-tooling.md).

## Design research

- [Extension contracts: reuse, avoid, unresolved](docs/research/extension-contract.md)
- [Web UI foundation: technology and interaction direction](docs/research/web-ui-foundation.md)
