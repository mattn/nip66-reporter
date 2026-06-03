// Command nip66-reporter is a NIP-66 relay monitor. It probes Nostr
// relays, publishes kind 30166 relay discovery events describing their
// measured characteristics, and announces itself with a kind 10166 monitor
// announcement.
//
// It runs as a daemon by default, re-probing every -frequency seconds, or it
// can run a single pass with -once (suitable for cron / systemd timers).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const (
	// KindRelayDiscovery is the NIP-66 relay discovery event.
	KindRelayDiscovery = 30166
	// KindRelayMonitor is the NIP-66 relay monitor announcement.
	KindRelayMonitor = 10166
)

const version = "0.0.1"

// revision is set at build time via -ldflags "-X main.revision=...".
var revision = "HEAD"

type arrayFlags []string

func (a *arrayFlags) String() string { return strings.Join(*a, ",") }

func (a *arrayFlags) Set(v string) error {
	*a = append(*a, v)
	return nil
}

// loadSecretKey resolves the monitor's secret key from the flag value, falling
// back to the NOSTR_SECRET_KEY environment variable. Both hex and nsec forms
// are accepted.
func loadSecretKey(sk string) (string, error) {
	if sk == "" {
		sk = os.Getenv("NOSTR_SECRET_KEY")
	}
	if sk == "" {
		return "", fmt.Errorf("no secret key (use -sk or NOSTR_SECRET_KEY)")
	}
	if strings.HasPrefix(sk, "nsec1") {
		_, v, err := nip19.Decode(sk)
		if err != nil {
			return "", fmt.Errorf("decode nsec: %w", err)
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("decode nsec: unexpected type %T", v)
		}
		return s, nil
	}
	return sk, nil
}

func main() {
	var publishRelays arrayFlags
	var monitorRelays arrayFlags
	var sk string
	var monitorFile string
	var frequency int
	var timeout time.Duration
	var concurrency int
	var once bool
	var discover bool
	var dryRun bool
	var showVersion bool

	flag.Var(&publishRelays, "relay", "relay to publish 30166/10166 events to (repeatable)")
	flag.Var(&monitorRelays, "monitor", "relay URL to monitor (repeatable)")
	flag.StringVar(&sk, "sk", "", "monitor secret key (hex or nsec); defaults to $NOSTR_SECRET_KEY")
	flag.StringVar(&monitorFile, "monitor-file", "", "file with relay URLs to monitor, one per line")
	flag.IntVar(&frequency, "frequency", 3600, "seconds between probe rounds in daemon mode")
	flag.DurationVar(&timeout, "timeout", 5*time.Second, "per-check probe timeout")
	flag.IntVar(&concurrency, "concurrency", 8, "number of relays probed in parallel")
	flag.BoolVar(&once, "once", false, "probe once and exit (for cron / systemd timers)")
	flag.BoolVar(&discover, "discover", false, "discover additional relays from 30166/10002 on publish relays")
	flag.BoolVar(&dryRun, "dry-run", false, "print events instead of publishing them")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("nip66-reporter v%s (%s)\n", version, revision)
		return
	}

	seckey, err := loadSecretKey(sk)
	if err != nil {
		log.Fatal(err)
	}
	pubkey, err := nostr.GetPublicKey(seckey)
	if err != nil {
		log.Fatalf("derive public key: %v", err)
	}

	if len(publishRelays) == 0 && !dryRun {
		log.Fatal("no publish relays (use -relay or -dry-run)")
	}

	m := &monitor{
		seckey:        seckey,
		pubkey:        pubkey,
		publishRelays: publishRelays,
		seedRelays:    monitorRelays,
		monitorFile:   monitorFile,
		frequency:     frequency,
		timeout:       timeout,
		concurrency:   concurrency,
		discover:      discover,
		dryRun:        dryRun,
	}

	ctx := context.Background()

	// Announce the monitor once up front.
	if err := m.announce(ctx); err != nil {
		log.Printf("announce: %v", err)
	}

	if once {
		if err := m.round(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}

	for {
		if err := m.round(ctx); err != nil {
			log.Printf("round: %v", err)
		}
		time.Sleep(time.Duration(frequency) * time.Second)
	}
}
