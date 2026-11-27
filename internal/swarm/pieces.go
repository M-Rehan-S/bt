package swarm

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/M-Rehan-S/bt/internal/wire"
)

const (
	maxPipeline    = 5
	requestTimeout = 10 * time.Second
)

type pieceStatus uint8

const (
	pieceNeeded pieceStatus = iota
	pieceInProgress
	pieceDone
)

type blockKey struct {
	idx   uint32
	begin uint32
}

type pieceDownload struct {
	state          pieceStatus
	owner          *peerConn
	buf            []byte
	blocksDone     []bool
	nextBlock      int
	receivedBlocks int
	totalBlocks    int
}

func (p *pieceDownload) reset() {
	p.state = pieceNeeded
	p.owner = nil
	p.buf = nil
	p.blocksDone = nil
	p.nextBlock = 0
	p.receivedBlocks = 0
	p.totalBlocks = 0
}

func numBlocksInPiece(pieceSize int) int {
	if pieceSize <= 0 {
		return 0
	}
	return (pieceSize + maxBlockLen - 1) / maxBlockLen
}

func blockSize(pieceSize, blockIdx int) int {
	rem := pieceSize - blockIdx*maxBlockLen
	if rem > maxBlockLen {
		return maxBlockLen
	}
	return rem
}

func initPieces(numPieces int, seed bool) []pieceDownload {
	pieces := make([]pieceDownload, numPieces)
	if seed {
		for i := range pieces {
			pieces[i].state = pieceDone
		}
	}
	return pieces
}

func (c *coordinator) fillAllPipelines() {
	if c.cfg.Seed || c.completed {
		return
	}
	for _, ps := range c.peers {
		if !ps.peerChoking {
			c.fillPipeline(ps)
		}
	}
}

func (c *coordinator) fillPipeline(ps *peerState) {
	if c.cfg.Seed || c.completed || ps.peerChoking {
		return
	}

	for len(ps.inflight) < maxPipeline {
		idx := c.nextPieceForPeer(ps)
		if idx < 0 {
			return
		}
		p := &c.pieces[idx]
		psize := c.store.PieceSize(idx)
		blockIdx := p.nextBlock
		begin := uint32(blockIdx * maxBlockLen)
		length := uint32(blockSize(psize, blockIdx))

		key := blockKey{idx: uint32(idx), begin: begin}
		p.nextBlock++
		now := time.Now()
		ps.inflight[key] = now
		ps.lastRequestAt = now

		if !ps.pc.send(outItem{msg: wire.NewRequest(uint32(idx), begin, length)}) {
			return
		}
	}
}

func (c *coordinator) nextPieceForPeer(ps *peerState) int {
	if ps.currentPiece >= 0 && ps.currentPiece < c.numPieces {
		p := &c.pieces[ps.currentPiece]
		if p.state == pieceInProgress && p.owner == ps.pc && p.nextBlock < p.totalBlocks {
			return ps.currentPiece
		}
		ps.currentPiece = -1
	}

	// Uniform random selection among pieces the peer has that are still needed.
	var candidates []int
	for i := 0; i < c.numPieces; i++ {
		if c.pieces[i].state == pieceNeeded && ps.bitfield.Has(i) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return -1
	}

	idx := candidates[c.rng.IntN(len(candidates))]
	psize := c.store.PieceSize(idx)
	totalBlocks := numBlocksInPiece(psize)
	if totalBlocks == 0 {
		return -1
	}

	p := &c.pieces[idx]
	p.state = pieceInProgress
	p.owner = ps.pc
	p.buf = make([]byte, psize)
	p.blocksDone = make([]bool, totalBlocks)
	p.nextBlock = 0
	p.receivedBlocks = 0
	p.totalBlocks = totalBlocks
	ps.currentPiece = idx
	return idx
}

func (c *coordinator) releasePeerWork(ps *peerState) {
	clear(ps.inflight)
	ps.currentPiece = -1
	for i := range c.pieces {
		if c.pieces[i].state == pieceInProgress && c.pieces[i].owner == ps.pc {
			c.pieces[i].reset()
		}
	}
}

func (c *coordinator) sweepRequestTimeouts(now time.Time) {
	released := false
	for _, ps := range c.peers {
		if len(ps.inflight) == 0 {
			continue
		}
		timedOut := false
		for _, sentAt := range ps.inflight {
			if now.Sub(sentAt) >= requestTimeout {
				timedOut = true
				break
			}
		}
		if timedOut {
			c.releasePeerWork(ps)
			released = true
		}
	}
	if released {
		c.fillAllPipelines()
	}
}

func (c *coordinator) handlePiece(ps *peerState, msg *wire.Message) error {
	idx, begin, data, err := wire.ParsePiece(msg)
	if err != nil {
		return nil
	}
	if idx >= uint32(c.numPieces) {
		return nil
	}
	key := blockKey{idx: idx, begin: begin}
	if _, requested := ps.inflight[key]; !requested {
		return nil
	}

	pIdx := int(idx)
	p := &c.pieces[pIdx]
	if p.state != pieceInProgress || p.owner != ps.pc {
		delete(ps.inflight, key)
		return nil
	}
	if begin%maxBlockLen != 0 {
		return nil
	}
	blockIdx := int(begin) / maxBlockLen
	if blockIdx < 0 || blockIdx >= p.totalBlocks || p.blocksDone[blockIdx] {
		return nil
	}
	wantLen := blockSize(len(p.buf), blockIdx)
	if len(data) != wantLen {
		return nil
	}

	delete(ps.inflight, key)
	c.stats.AddDownBytes(int64(len(data)))
	copy(p.buf[int(begin):int(begin)+len(data)], data)
	p.blocksDone[blockIdx] = true
	p.receivedBlocks++

	if p.receivedBlocks == p.totalBlocks {
		if c.store.VerifyPiece(pIdx, p.buf) {
			if err := c.store.WritePiece(pIdx, p.buf); err != nil {
				return fmt.Errorf("write verified piece %d: %w", pIdx, err)
			}
			p.state = pieceDone
			p.owner = nil
			p.buf = nil
			p.blocksDone = nil
			if ps.currentPiece == pIdx {
				ps.currentPiece = -1
			}
			c.ourBitfield.Set(pIdx)
			c.donePieces++

			c.logger.PieceOK(pIdx, ps.pc.addr)

			// Broadcast have to all connected peers and re-evaluate interest.
			for otherPC, otherPS := range c.peers {
				otherPC.send(outItem{msg: wire.NewHave(idx)})
				c.updateInterest(otherPS)
			}

			if c.donePieces == c.numPieces && !c.completed {
				c.completed = true
				c.logger.Complete()
			}
		} else {
			c.logger.HashFail(pIdx, ps.pc.addr)
			p.reset()
			if ps.currentPiece == pIdx {
				ps.currentPiece = -1
			}
			c.fillAllPipelines()
			return nil
		}
	}

	c.fillPipeline(ps)
	return nil
}

func newCoordinatorRNG() *rand.Rand {
	seed := uint64(time.Now().UnixNano())
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}
