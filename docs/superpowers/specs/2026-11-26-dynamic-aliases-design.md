# Address aliases fed from a URL or from host names (#115)

The owner's requirements and decisions are in #115. This is how Andaru meets them.

## What is saved

Two more kinds of address alias, beside `host`, `network`, `range` and `group`:

- **`url`:** `{"kind":"url","url":"https://www.cloudflare.com/ips-v4","refreshSeconds":3600}`. The list holds addresses and networks, IPv4 or IPv6, one per line; blank lines and lines starting with `#` are ignored.
- **`hostnames`:** `{"kind":"hostnames","names":["vpn.example.com"],"refreshSeconds":300}`. Each name is resolved for A and AAAA records.

`refreshSeconds` is from 60 to 86400. A URL is `https`, or `http` with a warning that anyone on the path can change the list, and with it what the rules allow; the warning does not depend on where the server is. At most 20 names.

Their members are not saved: a refresh is not a configuration change, so it needs no apply, makes no generation and asks for no confirmation.

In this first version a URL or host-name alias may be referred to by rules and port forwards, but not be a group's member nor a management source. Groups and the management sets copy their members' addresses into sets of their own, which a refresh would have to follow; that comes when asked for.

## How it reaches nftables

A URL or host-name alias always renders both of its family sets, `adr_<id>_4` and `adr_<id>_6`, declared with `flags interval` and no elements, so a rule naming it has a line for each family whatever the list holds. An alias that never fetched matches nothing.

The engine keeps each alias's last good list in `/var/lib/andaru/aliases/<id>.json`. A root `andaru realize aliases` verb flushes each dynamic alias's sets and adds the saved members, in one `nft -f` transaction. Every load of the filter (activate, rollback, boot) runs the same fill right after it, since a load empties the sets.

## Who fetches

`andaru-outbound` fetches and resolves; the engine has no network.

- The engine schedules: on each alias's interval, and on "Refresh now", it publishes a durable request on `alias-fetch/<id>` with the URL or names.
- `andaru-outbound` reads its consumer on `alias-fetch/#` and publishes the outcome on `alias-result/<id>`: the parsed members, or why it failed.
  - HTTPS with the certificate verified. Redirects are followed, at most five and never downwards: from HTTPS to HTTPS only; an HTTP address may go up to HTTPS. A refused redirect fails the fetch.
  - A status other than 200, a body over 1 MiB, more than 10,000 entries, or a body that is not a list (an HTML page) fails the fetch. A 200 with an empty body is an empty list.
  - A name that does not exist (NXDOMAIN) fails, as no answer does.
  - The body is parsed here, in the unprivileged process.
- The engine checks the members again, writes the state file and runs `realize aliases` when the list changed.

**A failed fetch never replaces the list.** It is recorded with its reason, and the last good list stays in use. After three failures in a row the engine raises `notification/alias` with the key `alias/<id>`; the next success clears it. Every fetch and lookup is logged: a success as information, with the entry count and whether the list changed; a failure as a warning, with the reason.

When an alias's members change, the engine publishes `alias/<id>`, not durable, for add-ons.

## What the API shows

For each URL or host-name alias: when it last succeeded, how many entries it holds, the last error and how many failures in a row, and for a host-name alias what each name last resolved to. `POST /api/v1/aliases/addresses/{id}/refresh` asks for a fetch now.

## The work, in order

1. The two kinds in the saved configuration, their validation, and their empty sets in the rendered filter.
2. The state files, `realize aliases`, the fill after each load, the engine's scheduler, result handling, notifications, the status API and "Refresh now".
3. The fetcher and resolver in `andaru-outbound`.
