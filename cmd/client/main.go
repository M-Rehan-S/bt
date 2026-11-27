package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"syscall"

	"github.com/M-Rehan-S/bt/internal/metainfo"
	"github.com/M-Rehan-S/bt/internal/swarm"
)

func main() {
	torrentPath := flag.String("torrent", "", "path to the .torrent file")
	outPath := flag.String("out", "", "output file path (or existing file for --seed)")
	seed := flag.Bool("seed", false, "start with the complete file as a seed")
	noUpload := flag.Bool("no-upload", false, "never unchoke anyone (client-server mode)")
	port := flag.Int("port", 6881, "listen port")
	name := flag.String("name", "", "peer label used in logs")
	logPath := flag.String("log", "", "path of the JSONL log file")
	trackerURL := flag.String("tracker", "", "optional override of the tracker announce URL")
	peersFlag := flag.String("peers", "", "optional comma-separated ip:port list, bypassing tracker")
	flag.Parse()

	if *torrentPath == "" || *outPath == "" {
		log.Fatal("usage: client --torrent FILE --out PATH [options]")
	}

	torrent, err := metainfo.Load(*torrentPath)
	if err != nil {
		log.Fatalf("load torrent: %v", err)
	}

	peerName := *name
	if peerName == "" {
		if *seed {
			peerName = "seed"
		} else {
			peerName = "client"
		}
	}

	peerID, err := newPeerID()
	if err != nil {
		log.Fatalf("generate peer id: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := swarm.Config{
		Torrent:     torrent,
		OutPath:     *outPath,
		Seed:        *seed,
		NoUpload:    *noUpload,
		Port:        *port,
		Name:        peerName,
		LogPath:     *logPath,
		TrackerURL:  *trackerURL,
		StaticPeers: parseStaticPeers(*peersFlag),
		PeerID:      peerID,
	}

	if err := swarm.Run(ctx, cfg); err != nil {
		log.Fatalf("client failed: %v", err)
	}
}

func newPeerID() ([20]byte, error) {
	var id [20]byte
	copy(id[:8], "-RZ0001-")
	if _, err := rand.Read(id[8:]); err != nil {
		return id, fmt.Errorf("rand read: %w", err)
	}
	return id, nil
}

func parseStaticPeers(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var peers []string
	for _, part := range strings.Split(raw, ",") {
		addr := strings.TrimSpace(part)
		if addr != "" {
			peers = append(peers, addr)
		}
	}
	return peers
}
