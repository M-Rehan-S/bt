# LOGSPEC.md: interfaces and conventions

Single source of truth for CLI flags, network layout, log format and metric
definitions. The analysis scripts depend on this file, so update it whenever
any of these things change.

## 1. Constants

| Name | Value |
|---|---|
| File size | 52,428,800 B (50 MiB = 419.43 Mb) |
| Piece length | 262,144 B (256 KiB), 200 pieces |
| Block length | 16,384 B (16 KiB), 16 blocks per piece |
| Request pipeline window | 5 outstanding block requests per peer |
| Unchoke slots | 4 interested peers, chosen at random on each rechoke |
| Rechoke interval | 3 s |
| Tracker interval | 2 s (returned in every announce response) |
| Max accepted wire message | 131,072 B (1<<17) |
| Seed upload cap u_s | 20 Mbps |
| Leecher upload cap u_i | 5 Mbps |
| Download caps | none (d_min uncapped) |

## 2. Network conventions (Mininet)

Mininet assigns IPs in the order hosts are added. The harness must always add
hosts in this order:

| Host | IP | Notes |
|---|---|---|
| tracker | 10.0.0.1 | listens on :8000 |
| seed | 10.0.0.2 | listens on :6881 |
| l1 ... lN | 10.0.0.3 ... | each listens on :6881 |

- Every host has its own network namespace, so all peers can use port 6881.
- The announce URL baked into the .torrent is `http://10.0.0.1:8000/announce`.
- Peer ID: 20 bytes = `-RZ0001-` + 12 random bytes.
- For localhost testing: `--tracker http://127.0.0.1:8000/announce` and
  different `--port` values per process.

## 3. Binaries and CLI

### mktorrent
```
mktorrent --file testdata/payload.bin --announce http://10.0.0.1:8000/announce --out payload.torrent
```
Piece length is fixed at 262144.

### tracker
```
tracker --port 8000 --interval 2
```

### client

| Flag | Meaning |
|---|---|
| `--torrent` | path to the .torrent file |
| `--out` | output file path (for `--seed`, the existing complete file) |
| `--seed` | start with the complete file, acting as the seed |
| `--no-upload` | never unchoke anyone (client-server mode) |
| `--port` | listen port, default 6881 |
| `--name` | label used in logs and filenames, e.g. `seed`, `l3` |
| `--log` | path of the JSONL log file |
| `--tracker` | optional override of the announce URL |
| `--peers` | optional static `ip:port,ip:port` list, bypasses the tracker (early testing) |

Behaviour rules:
- A leecher keeps running after it completes (it keeps seeding in P2P mode)
  until the harness kills it.
- A seed sends a full bitfield and never sends `interested`.
- In `--no-upload` mode the client never sends `unchoke` to anyone.
- The output file is pre-allocated to full size and each verified piece is
  written with `WriteAt(idx * pieceLen)`.

## 4. Tracker protocol

Request:
```
GET /announce?info_hash=<20 raw bytes, percent-encoded>&peer_id=<20 bytes>
    &port=6881&uploaded=0&downloaded=0&left=<bytes>&event=started&compact=1
```
- `event` is `started` on the first announce, empty on later ones.
- The peer IP is taken from the TCP connection (`r.RemoteAddr`), not from a parameter.

Response (bencoded dict):
```
d8:intervali2e5:peers12:<12 raw bytes>e
```
- `peers` is a byte string, 6 bytes per peer: 4 bytes IPv4 + 2 bytes port, big-endian.
- The requesting peer is excluded from its own list.
- The client re-announces every `interval` seconds and connects to any peer
  it is not already connected to (dedupe by `ip:port`).

## 5. Parsed torrent (shared Go struct)

```go
type Torrent struct {
    Announce    string
    Name        string
    Length      int64
    PieceLength int
    PieceHashes [][20]byte
    InfoHash    [20]byte   // SHA-1 of the bencoded info dict
}
```

## 6. Log format

One JSON object per line, written to the file given by `--log`.

Common fields on every line:
- `t_ns`: `time.Now().UnixNano()`. All Mininet hosts share one kernel clock, so
  timestamps from different hosts are directly comparable.
- `peer`: the value of `--name`.
- `ev`: the event name.

| `ev` | Extra fields | When |
|---|---|---|
| `start` | none | Once, right after startup and before the first announce. |
| `piece_ok` | `idx`, `from` (`ip:port`) | A piece's SHA-1 verified and was written to disk. |
| `hash_fail` | `idx`, `from` | A piece failed verification (it is discarded and re-requested). |
| `piece_sent` | `idx`, `to` (`ip:port`) | Optional. Logged when the **last block** of a piece has been sent to a peer. Used for the duplicate-seed-upload analysis. |
| `stats` | `down_bytes`, `up_bytes`, `down_wire`, `up_wire` | Every 1 s, **cumulative** counters. |
| `complete` | none | Once, when the last piece verifies. Seeds never log this. |

Byte-counting rules:
- `down_bytes` / `up_bytes` count **payload only**: the `data` field of `piece`
  messages, nothing else.
- `down_wire` / `up_wire` count **every byte read from / written to the TCP
  connections** at the application level (handshakes, message headers, `have`,
  `request`, and so on), excluding TCP/IP headers. This is used to estimate protocol overhead.
- Counters are cumulative since process start, never deltas.
- Bytes from a piece that fails verification still count as downloaded.
- The seed logs `stats` too. Its `up_bytes` is used for the offload ratio.

## 7. Metric definitions

Let `T0 = min(start.t_ns)` over all leechers of a run, and let the leechers be
`l1..lN`.

| Metric | Definition |
|---|---|
| Distribution time D | `(max(complete.t_ns over leechers) - T0) / 1e9`, in seconds |
| Per-peer throughput | Difference of consecutive `down_bytes` values divided by the elapsed time, converted to Mbps (`bytes*8/1e6`) |
| Seed offload ratio | `1 - seed_up_bytes / sum(leecher_down_bytes)`, using each peer's last `stats` line at or before the time of the last `complete`. Equals 0 in client-server mode by construction. |
| Integrity | Count of `hash_fail` events; wasted bytes = count x 262,144 |
| Wire overhead | `(down_wire - down_bytes) / down_bytes` per leecher, from the last stats line |
| Overhead gap | `D_measured / D_bound`, with the bounds computed using F = 419.43 Mb |
| Duplicate seed uploads | Number of `piece_sent` events on the seed beyond the first for each `idx` |

A run is **valid** only if all N leechers logged `complete` before the timeout
and the `sha1sum` of every output file equals that of `testdata/payload.bin`.
Invalid runs are discarded and re-run, and are never averaged in.

## 8. Results layout

```
results/{mode}_N{n}_run{k}/
    seed.jsonl
    l1.jsonl ... lN.jsonl
    tracker.log
    hashes.txt            # sha1sum of each output file
    out/<host>/payload.bin
```

`mode` is `cs` or `p2p`.

The run matrix is N in {2, 4, 8, 16} x mode in {cs, p2p} x runs {1, 2, 3}, with
the order interleaved so that drift does not bias one mode.

## 9. Fallbacks (decided in advance)

- If time is short: 2 repetitions instead of 3.
- If still short: use the formula for the CS N=16 point instead of running it,
  and state this clearly in the report.