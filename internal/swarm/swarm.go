package swarm

import (
	"context"
	"time"

	"github.com/M-Rehan-S/bt/internal/metainfo"
)

// Config holds the runtime parameters for one BitTorrent client instance.
type Config struct {
	Torrent     *metainfo.Torrent
	OutPath     string
	Seed        bool
	NoUpload    bool
	Port        int
	Name        string
	LogPath     string
	TrackerURL  string   // override; empty means Torrent.Announce
	StaticPeers []string // "ip:port"; empty means use tracker
	PeerID      [20]byte // "-RZ0001-" + 12 random bytes
}

// Run initializes storage, verification, and logging, then blocks until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	store, err := OpenStorage(cfg.OutPath, cfg.Torrent, cfg.Seed)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, err := NewLogger(cfg.LogPath, cfg.Name)
	if err != nil {
		return err
	}
	defer logger.Close()

	stats := &Stats{}
	logger.Start()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Stats(stats.Snapshot())
			return nil
		case <-ticker.C:
			logger.Stats(stats.Snapshot())
		}
	}
}
