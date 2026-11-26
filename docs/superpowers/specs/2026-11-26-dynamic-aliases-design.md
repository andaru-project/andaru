# Address aliases fed from a URL or from host names (#115)

The owner's requirements and decisions are in #115. This is how Andaru meets them.

## What is saved

Two more kinds of address alias, beside `host`, `network`, `range` and `group`:

- **`url`:** `{"kind":"url","url":"https://www.cloudflare.com/ips-v4","refreshSeconds":3600}`. The list holds addresses and networks, IPv4 or IPv6, one per line; blank lines and lines starting with `#` are ignored.
- **`hostnames`:** `{"kind":"hostnames","names":["vpn.example.com"],"refreshSeconds":300}`. Each name is resolved for A and AAAA records.

`refreshSeconds` is from 60 to 86400. A URL is `https`, or `http` with a warning that anyone on the path can change the list, and with it what the rules allow; the warning does not depend on where the server is. At most 20 names.

Their members are not saved: a refresh is not a configuration change, so it needs no apply, makes no generation and asks for no confirmation.

In this first version a URL or host-name alias may be referred to by rules, port forwards and 1:1 destinations, but not be a group's member nor a management source, and not be left out (a rule's `not`, a forward's negated source or destination, a negated 1:1 destination): its set is empty until a list is fetched, and leaving out an empty set lets every address through. Groups and the management sets copy their members' addresses into sets of their own, which a refresh would have to follow; that comes when asked for.

## How it reaches nftables

A URL or host-name alias always renders both of its family sets, `adr_<id>_4` and `adr_<id>_6`, declared with `flags interval` and no elements, so a rule naming it has a line for each family whatever the list holds. An alias that never fetched matches nothing, which is why it may not be left out; and a `drop` or `reject` rule naming it drops nothing until its first fetch (the owner's rule).

The members belong to a **source**: the alias's URL, or its set of names. Its hash is the first 16 hex digits of a SHA-256 of the kind and the URL, or of the kind and the names, sorted and lower-cased. Both sets carry it, `comment "feed:<hash>"`, so a filter says which source it was rendered for. A changed URL or set of names is a new source, which starts with an empty list: an old source's list never stands for a new one.

The engine keeps each source's last good list in `/var/lib/andaru/aliases/<id>/<hash>.json`. A root `andaru realize aliases` verb finds the dynamic sets of the live generation's filter and their hashes, flushes each, and adds the members of that source, in one `nft -f` transaction; a source with no state is flushed only. Every load of the filter (activate, rollback, boot) does the same for the filter it loads, in the same transaction as the new table, so the sets are never empty between a load and a fill. A saved change, not applied, never touches the live sets.

A state file that does not decode is renamed aside, to `<hash>.json.corrupt-<unix seconds>`, logged, and the source starts again from an empty state.

## Who fetches

`andaru-outbound` fetches and resolves; the engine has no network.

- The engine schedules every source the saved head or the live generation names (the live one by the saved version it was applied from): so an apply finds its new list already fetched, and the live list keeps refreshing until an apply replaces it. On each source's interval, and on "Refresh now" (the saved source and the live one, at most once in 10 seconds per alias), it records a request in the source's state, then publishes it, durable, on `alias-fetch/<id>` with the hash and the URL or names. A request that cannot be recorded is not sent. A source waiting for an answer is not asked again before its interval, and keeps at most 16 requests waiting.
- `andaru-outbound` reads its consumer on `alias-fetch/#` and publishes the outcome on `alias-result/<id>`: the parsed members, or why it failed.
  - HTTPS with the certificate verified. Redirects are followed, at most five and never downwards: from HTTPS to HTTPS only; an HTTP address may go up to HTTPS. A refused redirect fails the fetch.
  - A status other than 200, a body over 1 MiB, more than 10,000 entries, or a body that is not a list (an HTML page) fails the fetch. A 200 with an empty body is an empty list.
  - A name that does not exist (NXDOMAIN) fails, as no answer does.
  - The body is parsed here, in the unprivileged process.
- The engine checks the members again, writes the state file of the source the result's hash names and runs `realize aliases` when the live filter's list changed. A result for a source neither generation names, or for a request already answered or no longer waited for, is dropped.
- A source neither the saved head nor the live generation names keeps its state 30 days after its last attempt, so a rollback or the boot of a kept generation finds its list; then the state is removed. A saved change never removes one.

**A failed fetch never replaces the list.** It is recorded with its reason, and the last good list stays in use. After three failures in a row the engine raises `notification/alias` with the key `alias/<id>`; the next success clears it, and so does an alias that neither the saved head nor the live generation names any more. A host-name alias fails only when every name fails; each name's error is still recorded and shown, and a name answered with no addresses and no error failed ("no addresses"). Every fetch and lookup is logged: a success as information, with the entry count and whether the list changed; a failure as a warning, with the reason.

When an alias's members change, the engine publishes `alias/<id>`, not durable, for add-ons.

## What the API shows

For each URL or host-name alias of the saved head, from its saved source: when it last succeeded, how many entries it holds, the last error and how many failures in a row, and for a host-name alias what each name last resolved to. `POST /api/v1/aliases/addresses/{id}/refresh` asks for a fetch now.

## The work, in order

1. The two kinds in the saved configuration, their validation, and their empty sets in the rendered filter.
2. The state files, `realize aliases`, the fill after each load, the engine's scheduler, result handling, notifications, the status API and "Refresh now". Built.
3. The fetcher and resolver in `andaru-outbound`.
