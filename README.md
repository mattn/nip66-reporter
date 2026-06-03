# nip66-reporter

A [NIP-66](https://github.com/nostr-protocol/nips/blob/master/66.md) relay
monitor. It probes Nostr relays, then publishes `kind 30166` relay discovery
events describing their measured characteristics and announces itself with a
`kind 10166` monitor announcement.

In NIP-66 terms this is the **reporter** (monitor) side: it observes relays from
the outside and publishes what it measured. Clients and directories consume
those `30166` events to discover and select relays. See
[nip66-viewer](https://github.com/mattn/nip66-viewer) for the consumer side.

## What it measures

Each probe produces a `30166` event with:

- `d` — the normalized relay URL
- `n` — network type (`clearnet`, `tor`, `i2p`, `loki`)
- `rtt-open` — websocket connection round-trip time (ms)
- `rtt-read` — stored-event read round-trip time, i.e. time to `EOSE` (ms)
- `N` — supported NIPs (from the relay's NIP-11 document)
- `R` — NIP-11 limitation-derived requirements (`auth`, `payment`, `writes`,
  `pow`; a `!` prefix means false)
- `t` — relay topics
- `content` — the relay's full NIP-11 document

## Install

```sh
go install github.com/mattn/nip66-reporter@latest
```

## Usage

The monitor signs everything with its own key, which is its identity. Use a
**dedicated** keypair, not your personal account.

```sh
export NOSTR_SECRET_KEY=nsec1...   # the monitor's key (hex also accepted)

# Probe a fixed set of relays once and publish the reports (good for cron).
nip66-reporter \
  -relay wss://yabu.me \
  -monitor wss://relay.damus.io \
  -monitor wss://nos.lol \
  -once
```

It runs as a daemon by default, re-probing every `-frequency` seconds. Use
`-once` for a single pass driven by cron or a systemd timer.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-relay` | — | relay to publish `30166`/`10166` to (repeatable) |
| `-monitor` | — | relay URL to monitor (repeatable) |
| `-monitor-file` | — | file with relay URLs to monitor, one per line |
| `-sk` | `$NOSTR_SECRET_KEY` | monitor secret key (hex or nsec) |
| `-frequency` | `3600` | seconds between probe rounds in daemon mode |
| `-timeout` | `5s` | per-check probe timeout |
| `-concurrency` | `8` | relays probed in parallel |
| `-discover` | `false` | discover more relays from `30166`/`10002` on the publish relays |
| `-once` | `false` | probe once and exit |
| `-dry-run` | `false` | print events instead of publishing |

### Relay list

Relays to probe come from `-monitor`, a `-monitor-file`, and — with
`-discover` — relays referenced by existing `30166` and `kind 10002` events on
the publish relays.

```sh
# relays.txt: one URL per line, # for comments
nip66-reporter -relay wss://yabu.me -monitor-file relays.txt -discover -once
```

## Running with systemd

```ini
# /etc/systemd/system/nip66-reporter.service
[Unit]
Description=NIP-66 relay reporter
After=network-online.target

[Service]
Environment=NOSTR_SECRET_KEY=nsec1...
ExecStart=/usr/local/bin/nip66-reporter -relay wss://yabu.me -monitor-file /etc/nip66/relays.txt -discover
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

For a cron-style run, drop the daemon and add `-once`, scheduled by a
`systemd.timer` or crontab.

## License

MIT

## Author

Yasuhiro Matsumoto (a.k.a. mattn)
