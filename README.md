<p align="center">
  <img src="assets/andaru-icon.svg" alt="Andaru" width="192">
</p>

# andaru
Andaru — a Free Software firewall platform for Debian

## Development

A two-VM QEMU lab (Debian 13 arm64, serial console only) is used for repeated,
reproducible testing against the two baselines Andaru must handle. The scripts
are in [`dev/vm-lab/`](dev/vm-lab); see [docs/dev/vm-lab.md](docs/dev/vm-lab.md)
for what it is, why it is serial-only, and how to run it.

```sh
dev/vm-lab/lab-setup
```
