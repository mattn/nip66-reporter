package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

type monitor struct {
	seckey        string
	pubkey        string
	publishRelays []string
	seedRelays    []string // fixed relays to monitor
	monitorFile   string
	frequency     int
	timeout       time.Duration
	concurrency   int
	discover      bool
	dryRun        bool
}

// targets returns the set of relay URLs to probe this round: the fixed seed
// list, any URLs from -monitor-file, and (when enabled) relays discovered from
// 30166/10002 events on the publish relays.
func (m *monitor) targets(ctx context.Context) []string {
	seen := map[string]bool{}
	var urls []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		u = nostr.NormalizeURL(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		urls = append(urls, u)
	}

	for _, u := range m.seedRelays {
		add(u)
	}
	if m.monitorFile != "" {
		b, err := os.ReadFile(m.monitorFile)
		if err != nil {
			log.Printf("monitor-file: %v", err)
		} else {
			for _, line := range strings.Split(string(b), "\n") {
				if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
					add(line)
				}
			}
		}
	}
	if m.discover {
		for _, u := range m.discoverRelays(ctx) {
			add(u)
		}
	}
	return urls
}

// discoverRelays queries the publish relays for existing 30166 discovery events
// and 10002 relay-list events, returning every relay URL referenced by them.
func (m *monitor) discoverRelays(ctx context.Context) []string {
	var found []string
	filters := nostr.Filters{
		{Kinds: []int{KindRelayDiscovery}, Limit: 500},
		{Kinds: []int{10002}, Limit: 500},
	}
	for _, ru := range m.publishRelays {
		qctx, cancel := context.WithTimeout(ctx, m.timeout)
		relay, err := nostr.RelayConnect(qctx, ru)
		if err != nil {
			cancel()
			continue
		}
		evs, err := relay.QuerySync(qctx, filters[0])
		if err == nil {
			for _, ev := range evs {
				if d := ev.Tags.GetFirst([]string{"d"}); d != nil && len(*d) > 1 {
					found = append(found, (*d)[1])
				}
			}
		}
		evs, err = relay.QuerySync(qctx, filters[1])
		if err == nil {
			for _, ev := range evs {
				for _, t := range ev.Tags {
					if len(t) > 1 && t[0] == "r" {
						found = append(found, t[1])
					}
				}
			}
		}
		relay.Close()
		cancel()
	}
	return found
}

// round probes every target relay and publishes a 30166 event for each.
func (m *monitor) round(ctx context.Context) error {
	urls := m.targets(ctx)
	if len(urls) == 0 {
		return fmt.Errorf("no relays to monitor (use -monitor, -monitor-file or -discover)")
	}
	log.Printf("probing %d relays", len(urls))

	sem := make(chan struct{}, m.concurrency)
	var wg sync.WaitGroup
	for _, url := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(url string) {
			defer wg.Done()
			defer func() { <-sem }()
			res := probe(ctx, url, m.timeout)
			if res.Err != nil {
				log.Printf("probe %s: %v", url, res.Err)
				return
			}
			ev := m.build30166(res)
			if err := m.publish(ctx, ev); err != nil {
				log.Printf("publish %s: %v", url, err)
				return
			}
			log.Printf("published %s (rtt-open=%dms read=%v nips=%d)",
				url, res.RTTOpen.Milliseconds(), res.ReadOK, nipCount(res))
		}(url)
	}
	wg.Wait()
	return nil
}

// build30166 converts a probe result into a signed kind 30166 event.
func (m *monitor) build30166(res result) *nostr.Event {
	tags := nostr.Tags{{"d", res.URL}, {"n", res.Network}}
	if res.RTTOpen > 0 {
		tags = append(tags, nostr.Tag{"rtt-open", ms(res.RTTOpen)})
	}
	if res.ReadOK {
		tags = append(tags, nostr.Tag{"rtt-read", ms(res.RTTRead)})
	}

	var content string
	if res.Info != nil {
		// N: supported NIPs.
		for _, n := range res.Info.SupportedNIPs {
			tags = append(tags, nostr.Tag{"N", nipString(n)})
		}
		// R: NIP-11 limitation-derived requirements (! prefix means false).
		if lim := res.Info.Limitation; lim != nil {
			tags = append(tags, nostr.Tag{"R", boolReq("auth", lim.AuthRequired)})
			tags = append(tags, nostr.Tag{"R", boolReq("payment", lim.PaymentRequired)})
			tags = append(tags, nostr.Tag{"R", boolReq("writes", lim.RestrictedWrites)})
			tags = append(tags, nostr.Tag{"R", boolReq("pow", lim.MinPowDifficulty > 0)})
		}
		// t: relay topics.
		for _, t := range res.Info.Tags {
			tags = append(tags, nostr.Tag{"t", t})
		}
		if b, err := json.Marshal(res.Info); err == nil {
			content = string(b)
		}
	}

	ev := &nostr.Event{
		PubKey:    m.pubkey,
		CreatedAt: nostr.Now(),
		Kind:      KindRelayDiscovery,
		Tags:      tags,
		Content:   content,
	}
	if err := ev.Sign(m.seckey); err != nil {
		log.Printf("sign 30166: %v", err)
	}
	return ev
}

// announce publishes the kind 10166 monitor announcement.
func (m *monitor) announce(ctx context.Context) error {
	timeoutMS := strconv.Itoa(int(m.timeout / time.Millisecond))
	ev := &nostr.Event{
		PubKey:    m.pubkey,
		CreatedAt: nostr.Now(),
		Kind:      KindRelayMonitor,
		Tags: nostr.Tags{
			{"frequency", strconv.Itoa(m.frequency)},
			{"timeout", "open", timeoutMS},
			{"timeout", "read", timeoutMS},
			{"timeout", "nip11", timeoutMS},
			{"c", "open"},
			{"c", "read"},
			{"c", "nip11"},
		},
		Content: "",
	}
	if err := ev.Sign(m.seckey); err != nil {
		return fmt.Errorf("sign 10166: %w", err)
	}
	return m.publish(ctx, ev)
}

// publish sends an event to every publish relay (or prints it in dry-run mode).
func (m *monitor) publish(ctx context.Context, ev *nostr.Event) error {
	if m.dryRun {
		b, _ := json.Marshal(ev)
		fmt.Println(string(b))
		return nil
	}
	var lastErr error
	for _, ru := range m.publishRelays {
		pctx, cancel := context.WithTimeout(ctx, m.timeout)
		relay, err := nostr.RelayConnect(pctx, ru)
		if err != nil {
			lastErr = err
			cancel()
			continue
		}
		if err := relay.Publish(pctx, *ev); err != nil {
			lastErr = err
		}
		relay.Close()
		cancel()
	}
	return lastErr
}

func ms(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}

// nipCount reports how many supported NIPs the probe found, for logging.
func nipCount(res result) int {
	if res.Info == nil {
		return 0
	}
	return len(res.Info.SupportedNIPs)
}

// boolReq formats a NIP-11 requirement, prefixing false values with "!".
func boolReq(name string, v bool) string {
	if v {
		return name
	}
	return "!" + name
}

// nipString renders a supported_nips entry (number or string) as a tag value.
func nipString(n any) string {
	switch v := n.(type) {
	case float64:
		return strconv.Itoa(int(v))
	case int:
		return strconv.Itoa(v)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}
