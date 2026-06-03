package main

import (
	"context"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

// result holds the measured characteristics of a single relay.
type result struct {
	URL      string
	Network  string // clearnet, tor, i2p, loki
	RTTOpen  time.Duration
	RTTRead  time.Duration
	ReadOK   bool
	Info     *nip11.RelayInformationDocument
	Err      error
}

// networkOf classifies a relay URL by its host suffix.
func networkOf(url string) string {
	host := url
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	switch {
	case strings.HasSuffix(host, ".onion"):
		return "tor"
	case strings.HasSuffix(host, ".i2p"):
		return "i2p"
	case strings.HasSuffix(host, ".loki"):
		return "loki"
	default:
		return "clearnet"
	}
}

// probe measures a single relay: its NIP-11 document, connection round-trip
// time (rtt-open) and a stored-event read round-trip time (rtt-read).
func probe(ctx context.Context, rawURL string, timeout time.Duration) result {
	url := nostr.NormalizeURL(rawURL)
	r := result{URL: url, Network: networkOf(url)}

	// NIP-11 document (best effort; failure does not abort the probe).
	infoCtx, cancel := context.WithTimeout(ctx, timeout)
	if info, err := nip11.Fetch(infoCtx, url); err == nil {
		r.Info = &info
	}
	cancel()

	// rtt-open: time to establish the websocket connection.
	connCtx, cancel := context.WithTimeout(ctx, timeout)
	start := time.Now()
	relay, err := nostr.RelayConnect(connCtx, url)
	cancel()
	if err != nil {
		r.Err = err
		return r
	}
	r.RTTOpen = time.Since(start)
	defer relay.Close()

	// rtt-read: time to receive EOSE for a tiny stored-event query.
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start = time.Now()
	sub, err := relay.Subscribe(readCtx, nostr.Filters{{Kinds: []int{1}, Limit: 1}})
	if err == nil {
		// Drain sub.Events while waiting: go-nostr dispatches messages
		// serially, so an unread EVENT blocks delivery of the following EOSE.
	loop:
		for {
			select {
			case <-sub.Events:
			case <-sub.EndOfStoredEvents:
				r.RTTRead = time.Since(start)
				r.ReadOK = true
				break loop
			case <-readCtx.Done():
				break loop
			}
		}
		sub.Close()
	}

	return r
}
