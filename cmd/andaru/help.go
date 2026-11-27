package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
)

// Every help line stays within 80 columns; help_test.go checks it.

const topUsage = `usage: andaru engine [OPTIONS]
       andaru ctl [--socket PATH] COMMAND [ARGUMENTS]
       andaru realize [--state-dir PATH] VERB [ARGUMENTS]
       andaru outbound [--bus-socket PATH] [--cache-dir PATH]
`

const topHelp = `Andaru is a Free Software firewall platform. The engine owns the saved
configuration and serves a root-only local API; ctl drives that API; realize
makes saved networking live on the host.

` + topUsage + `
Commands:
  engine    Run the engine: the local API, saved configuration and identities
  ctl       Read and change the configuration through the running engine
  realize   Privileged host steps the engine and systemd units run
  outbound  The unprivileged service that sends notifications

Run 'andaru <command> --help' for the options of a command.
`

const engineUsage = `usage: andaru engine [--socket PATH] [--state-dir PATH]
                     [--web-socket PATH --web-uid UID --web-gid GID
                      --web-origin HTTPS_ORIGIN ...]
`

const engineHelp = `Run the Andaru engine: it owns the state directory and serves the root-only
local API on a Unix socket. The web options also serve a dedicated socket to
andaru-web: give --web-socket and at least one --web-origin, and --web-uid
and --web-gid unless /etc/andaru/engine-web-peer holds them.

` + engineUsage + `
Options:
  --socket PATH        absolute local engine socket path
                       (default ` + defaultSocket + `)
  --state-dir PATH     absolute private state directory
                       (default ` + defaultStateDir + `)
  --web-socket PATH    absolute dedicated web engine socket path
  --web-uid UID        exact non-root static web process UID
  --web-gid GID        exact non-root static web socket group GID
  --web-origin HTTPS_ORIGIN
                       exact HTTPS management origin; repeat for each origin
`

const realizeUsage = `usage: andaru realize [OPTIONS] activate|confirm N
       andaru realize [OPTIONS] rollback N REASON
       andaru realize [OPTIONS] boot|leases|prefixes|aliases|factory-reset
       andaru realize [OPTIONS] update-check|update-stage
       andaru realize [OPTIONS] update-rollback|update-reboot
       andaru realize [OPTIONS] set-hostname NAME
`

const realizeHelp = `Privileged host steps. The engine runs them in transient systemd units, and
Andaru's own units and timers run the rest; they are not meant to be typed.

` + realizeUsage + `
Verbs:
  activate N         Make generation N live
  rollback N REASON  Roll generation N back to the previous one
  confirm N          Keep generation N as the confirmed one boot uses
  boot               Copy the confirmed generation into /run at boot
  leases             Publish DHCP lease names and route delegated prefixes
  prefixes           Follow the delegated IPv6 prefix into the DHCPv6 server
  aliases            Load the fetched URL and host-name alias lists into nft
  factory-reset      Run or resume the factory reset ctl factory-reset planned
  update-check       Check for signed appliance image updates and cache status
  update-stage       Download and stage the next signed appliance image
  update-rollback    Queue rollback to the previous appliance image
  update-reboot      Reboot the appliance into the staged deployment
  set-hostname NAME  Set the static appliance hostname via hostnamectl

Options:
  --state-dir PATH   absolute private state directory (default ` + defaultStateDir + `)
  --bus-socket PATH  the engine's event bus socket, told of each activate,
                     confirm and rollback; without it, the bus is not told
`

const outboundUsage = "usage: andaru outbound [--bus-socket PATH] [--cache-dir PATH]\n"

const outboundHelp = `Run andaru-outbound, the unprivileged service that talks to the outside world
for the engine: it reads delivery jobs from the event bus and sends them, and
fetches the lists of the address aliases fed from a URL or from host names.
Its systemd unit runs it as the andaru-outbound user.

` + outboundUsage + `
Options:
  --bus-socket PATH  the bus socket for Andaru's services
                     (default /run/andaru-services/bus.sock)
  --cache-dir PATH   where sent job IDs and jobs waiting for a retry are kept
                     (default $CACHE_DIRECTORY, else /var/cache/andaru-outbound)
`

const ctlUsage = "usage: andaru ctl [--socket PATH] COMMAND [ARGUMENTS]\n"

// ctlCommand is the help of one `andaru ctl` command: a one-line summary for the command
// list, the usage lines a usage error prints, and the details --help adds.
type ctlCommand struct{ name, summary, usage, details string }

var ctlCommands = []ctlCommand{
	{"links", "Show the host's network interfaces and their addresses",
		"usage: andaru ctl links\n",
		"\nThe interfaces the kernel reports now, as JSON on stdout.\n"},
	{"prefixes", "Show the delegated IPv6 prefixes and how they are used",
		"usage: andaru ctl prefixes\n",
		"\nEach WAN's requested length, its current prefix and lifetimes, each\ndelegated segment's /64 and each pool's range, as JSON on stdout.\n"},
	{"leases", "Show the DHCPv4 and DHCPv6 leases",
		"usage: andaru ctl leases\n",
		"\nThe DHCPv4 and DHCPv6 leases snapshot, as JSON on stdout.\n"},
	{"lldp", "Show live LLDP neighbors discovered on host interfaces",
		"usage: andaru ctl lldp\n",
		"\nThe LLDP neighbors systemd-networkd currently reports, as JSON on stdout.\n"},
	{"offloads", "Show the live offloads of the Ethernet devices",
		"usage: andaru ctl offloads\n",
		"\nWhether each offload Andaru manages runs on each Ethernet device, and whether\nthe device can change it, as JSON on stdout.\n"},
	{"openapi", "Print the engine API's OpenAPI description",
		"usage: andaru ctl openapi\n", ""},
	{"setup-code", "Show or rotate the one-time WebUI first-boot setup code",
		"usage: andaru ctl setup-code [--rotate]\n",
		"\nPrints the one-time setup code while no administrator account exists.\n"},
	{"config", "Read and change the saved configuration",
		`usage: andaru ctl config show
       andaru ctl config list|create COLLECTION
       andaru ctl config get COLLECTION ID
       andaru ctl config replace|delete --version V COLLECTION ID
       andaru ctl config get SINGLETON
       andaru ctl config replace|delete --version V SINGLETON
       andaru ctl config order LIST --version V ID...
       andaru ctl config changes
`, `
Commands:
  show     Print the whole saved configuration
  list     Print every object of a collection
  get      Print one object, or a singleton
  create   Create an object from the JSON document on stdin
  replace  Replace an object or singleton with the JSON document on stdin
  delete   Delete an object, or unset a singleton
  order    Set the order of a list: every entry's ID once, first to last
  changes  Save the batch of changes on stdin, all or nothing

Collections: links, segments, zones, aliases/addresses, aliases/ports, dhcp,
  dhcp6, virtualAddresses, notifications/channels, rules, nat/port-forwards,
  nat/one-to-one
Singletons: management, nat/outbound, dns
Lists: rules, nat/port-forwards, nat/one-to-one

Options:
  --version V  the version your last read returned; 0 sets a singleton
               that is not set yet

Example:
  printf '%s\n' '{"name":"lan"}' | sudo andaru ctl config create zones
`},
	{"versions", "List the saved versions and show one",
		`usage: andaru ctl versions list [--limit N] [--before V]
       andaru ctl versions get V
`, `
Commands:
  list  List saved versions, newest first
  get   Print one saved version and its changes

Options:
  --limit N   maximum saved versions, 1-100 (default 50)
  --before V  list only the versions older than V
`},
	{"preview", "Show the files a saved version would give the network",
		"usage: andaru ctl preview network [--saved-version N]\n", `
Shows the files Andaru would hand systemd-networkd and pppd, and what would
stop them working. It changes nothing.

Options:
  --saved-version N  saved version to render (default: the saved head)
`},
	{"apply", "Make the saved network live behind a rollback timer",
		"usage: andaru ctl apply network --saved-version N [--confirm-timeout S]\n", `
Unless confirmed from a new session in time, the previous network returns.

Options:
  --saved-version N    saved version to apply; must be the saved head
  --confirm-timeout S  seconds until the change is rolled back unless
                       confirmed, 30-600 (default 120)

Example:
  sudo andaru ctl apply network --saved-version 12
  sudo andaru ctl applies confirm 1   # from a NEW session
`},
	{"applies", "List applies; confirm or roll one back",
		`usage: andaru ctl applies list
       andaru ctl applies get|confirm|rollback ID
`, `
Commands:
  list      List every apply and the confirmed, last-known-good, pending
            and live pointers
  get       Show one apply
  confirm   Keep a pending apply; run it from a new session
  rollback  Undo a pending apply without waiting for its timer
`},
	{"backup", "Create, inspect and restore portable backups",
		`usage: andaru ctl backup create --output FILE [--include-secrets]
                                [--include-credentials]
       andaru ctl backup inspect --input FILE
       andaru ctl backup restore --input FILE --stores STORE[,STORE...]
                                 [--operation-id ID]
`, `
Commands:
  create   Write a backup of the saved stores; never overwrites FILE
  inspect  Validate a backup and print what it holds
  restore  Replace the selected saved stores from a backup, after a preview
           and typing RESTORE; it never applies networking

Options:
  --output FILE          backup file to create
  --input FILE           backup file to read
  --include-secrets      include secret values
  --include-credentials  include user password credentials
  --stores STORES        stores to restore: configuration,
                         configurationSecrets, users, userCredentials
  --operation-id ID      restore operation ID; reuse it to retry the same
                         restore (default: a new one)

With --include-secrets or --include-credentials, create asks for a
passphrase twice and encrypts the file; reading it asks for it again.

Example:
  sudo andaru ctl backup restore --input andaru.zip --stores configuration
`},
	{"kept", "Keep the saved network for after a factory reset",
		"usage: andaru ctl kept store-recovery\n", `
Commands:
  store-recovery  Keep the saved head's network as the network a factory
                  reset brings the box back on
`},
	{"users", "Manage user accounts",
		`usage: andaru ctl users bootstrap-admin --login LOGIN [--display-name NAME]
       andaru ctl users list [--cursor CURSOR] [--limit N]
       andaru ctl users get ID
       andaru ctl users create --login LOGIN --kind KIND --revision N
                               [--display-name NAME] [--roles ID,...]
                               [--disabled]
       andaru ctl users update ID --revision N [--login LOGIN]
                               [--display-name NAME] [--kind KIND]
                               [--roles ID,...] [--enabled true|false]
       andaru ctl users disable|delete|password-reset ID --revision N
`, `
Commands:
  bootstrap-admin  Create the first administrator; asks for the password
                   twice, then for BOOTSTRAP
  list             List accounts
  get              Show one account
  create           Create an account
  update           Change an account; give at least one field
  disable          Disable an account
  delete           Delete an account
  password-reset   Set an account's password; asks for it twice

Options:
  --login LOGIN        login name, 3-64 characters: letters, digits, '.',
                       '_' and '-', starting with a letter or digit; stored
                       in lower case
  --display-name NAME  display name, at most 128 characters
  --kind KIND          human or service
  --roles ID,...       role IDs, comma-separated
  --disabled           create the account disabled
  --enabled BOOL       true enables the account, false disables it
  --revision N         identity revision your last read returned
  --cursor CURSOR      continue from a previous page's nextCursor
  --limit N            maximum accounts, 1-100 (default 50)

These commands require root.
`},
	{"tokens", "Manage API tokens",
		`usage: andaru ctl tokens create --account-id ID --name NAME --scope CAP,...
                                [--expires-at RFC3339 | --no-expiry]
                                [--revoke-on-password-change true|false]
       andaru ctl tokens list --account-id ID [--cursor CURSOR] [--limit N]
       andaru ctl tokens revoke SELECTOR
`, `
Commands:
  create  Issue a token; its secret is shown once
  list    List an account's tokens
  revoke  Revoke a token by its selector

Options:
  --account-id ID                 account the token belongs to
  --name NAME                     token name, 1-128 characters
  --scope CAP,...                 capabilities the token grants
  --expires-at RFC3339            expiry time
  --no-expiry                     never expire; revoke it explicitly
  --revoke-on-password-change B   true revokes the token when the
                                  account's password changes
  --cursor CURSOR                 continue from a previous page's nextCursor
  --limit N                       maximum tokens, 1-50 (default 50)

These commands require root.
`},
	{"audit", "List the audit log",
		"usage: andaru ctl audit list [--cursor CURSOR] [--limit N]\n", `
Options:
  --cursor CURSOR  continue from a previous page's nextCursor
  --limit N        maximum events, 1-100 (default 50)

This command requires root.
`},
	{"factory-reset", "Erase Andaru's state and start over",
		"usage: andaru ctl factory-reset run|abort|status\n", `
Commands:
  run     Delete every account, setting, saved version, generation and audit
          record, and bring the box back on the stored recovery network,
          else on the last LAN an administrator used; interactive
  status  Show whether a factory reset is in progress
  abort   Cancel a factory reset that has not begun stopping Andaru

These commands require root.
`},
}

func findCtlCommand(name string) (ctlCommand, bool) {
	for _, c := range ctlCommands {
		if c.name == name {
			return c, true
		}
	}
	return ctlCommand{}, false
}

func ctlHelp(w io.Writer) {
	fmt.Fprint(w, `Read and change Andaru's configuration through the running engine. Success
is JSON on stdout, except for kept and factory-reset, which print text;
diagnostics go to stderr.

`+ctlUsage+`
Commands:
`)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range ctlCommands {
		fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.summary)
	}
	tw.Flush()
	fmt.Fprint(w, `
Options:
  --socket PATH  absolute local engine socket path
                 (default `+defaultSocket+`)

Run 'andaru ctl COMMAND --help' for the arguments of a command.
`)
}

func (c ctlCommand) help() string { return c.summary + ".\n\n" + c.usage + c.details }

// usageError prints a usage and the hint to its help, and returns the usage exit code.
func usageError(w io.Writer, usage, command string) int {
	fmt.Fprint(w, usage+"Run '"+command+" --help' for details.\n")
	return 2
}

// ctlUsageError prints the usage of the misused command, or of ctl for an unknown one.
func ctlUsageError(w io.Writer, name string) int {
	if c, ok := findCtlCommand(name); ok {
		return usageError(w, c.usage, "andaru ctl "+name)
	}
	return usageError(w, ctlUsage, "andaru ctl")
}

// isHelp reports a help flag, with or without a value (--help=true).
func isHelp(arg string) bool {
	arg, _, _ = strings.Cut(arg, "=")
	return arg == "-h" || arg == "-help" || arg == "--help"
}

// helpRequested reports a help flag anywhere among the arguments, even where a flag's value belongs.
func helpRequested(args []string) bool { return slices.ContainsFunc(args, isHelp) }
