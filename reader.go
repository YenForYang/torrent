package torrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"
)

// Accesses Torrent data via a Client. Reads block until the data is available. Seeks and readahead
// also drive Client behaviour.
type Reader interface {
	io.ReadSeekCloser
	missinggo.ReadContexter
	// Configure the number of bytes ahead of a read that should also be prioritized in preparation
	// for further reads.
	SetReadahead(int64)
	// Don't wait for pieces to complete and be verified. Read calls return as soon as they can when
	// the underlying chunks become available.
	SetResponsive()
}

// Piece range by piece index, [begin, end).
type pieceRange struct {
	begin, end pieceIndex
}

type reader struct {
	t          *Torrent
	responsive bool
	// Adjust the read/seek window to handle Readers locked to File extents and the like.
	offset, length int64

	// Required when modifying pos and readahead, or reading them without opMu.
	mu  sync.Locker
	pos int64
	// Reads have been initiated since the last seek. This is used to prevent readahead occuring
	// after a seek or with a new reader at the starting position.
	reading   bool
	readahead int64
	// Function to dynamically calculate readahead. If nil, readahead is static.
	readaheadFunc func() int64
	// Position that reads have continued contiguously from.
	contiguousReadStartPos int64
	// The cached piece range this reader wants downloaded. The zero value corresponds to nothing.
	// We cache this so that changes can be detected, and bubbled up to the Torrent only as
	// required.
	pieces pieceRange
}

var _ io.ReadSeekCloser = (*reader)(nil)

func (r *reader) SetResponsive() {
	r.responsive = true
	r.t.cl.event.Broadcast()
}

// Disable responsive mode. TODO: Remove?
func (r *reader) SetNonResponsive() {
	r.responsive = false
	r.t.cl.event.Broadcast()
}

func (r *reader) SetReadahead(readahead int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readahead = readahead
	r.readaheadFunc = nil
	r.posChanged()
}

// How many bytes are available to read. Max is the most we could require.
func (r *reader) available(off, max int64) (ret int64) {
	off += r.offset
	for max > 0 {
		req, ok := r.t.offsetRequest(off)
		if !ok {
			break
		}
		if !r.responsive && !r.t.pieceComplete(pieceIndex(req.Index)) {
			break
		}
		if !r.t.haveChunk(req) {
			break
		}
		len1 := int64(req.Length) - (off - r.t.requestOffset(req))
		max -= len1
		ret += len1
		off += len1
	}
	// Ensure that ret hasn't exceeded our original max.
	if max < 0 {
		ret += max
	}
	return
}

// Calculates the pieces this reader wants downloaded, ignoring the cached value at r.pieces.
func (r *reader) piecesUncached() (ret pieceRange) {
	ra := r.readahead
	if r.readaheadFunc != nil {
		ra = r.readaheadFunc()
	}
	if ra < 1 {
		// Needs to be at least 1, because [x, x) means we don't want
		// anything.
		ra = 1
	}
	if !r.reading {
		ra = 0
	}
	if ra > r.length-r.pos {
		ra = r.length - r.pos
	}
	ret.begin, ret.end = r.t.byteRegionPieces(r.torrentOffset(r.pos), ra)
	return
}

func (r *reader) Read(b []byte) (n int, err error) {
	return r.ReadContext(context.Background(), b)
}

func (r *reader) ReadContext(ctx context.Context, b []byte) (n int, err error) {
	if len(b) > 0 {
		r.reading = true
		// TODO: Rework reader piece priorities so we don't have to push updates in to the Client
		// and take the lock here.
		r.mu.Lock()
		r.posChanged()
		r.mu.Unlock()
	}
	n, err = r.readOnceAt(ctx, b, r.pos)
	if n == 0 {
		if err == nil && len(b) > 0 {
			panic("expected error")
		} else {
			return
		}
	}

	r.mu.Lock()
	r.pos += int64(n)
	r.posChanged()
	r.mu.Unlock()
	if r.pos >= r.length {
		err = io.EOF
	} else if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return
}

var closedChan = make(chan struct{})

func init() {
	close(closedChan)
}

// Wait until some data should be available to read. Tickles the client if it isn't. Returns how
// much should be readable without blocking.
func (r *reader) waitAvailable(ctx context.Context, pos, wanted int64, wait bool) (avail int64, err error) {
	t := r.t
	for {
		r.t.cl.rLock()
		avail = r.available(pos, wanted)
		readerCond := t.piece(int((r.offset + pos) / t.info.PieceLength)).readerCond.Signaled()
		r.t.cl.rUnlock()
		if avail != 0 {
			return
		}
		var dontWait <-chan struct{}
		if !wait || wanted == 0 {
			dontWait = closedChan
		}
		select {
		case <-r.t.closed.Done():
			err = errors.New("torrent closed")
			return
		case <-ctx.Done():
			err = ctx.Err()
			return
		case <-r.t.dataDownloadDisallowed.On():
			err = errors.New("torrent data downloading disabled")
		case <-r.t.networkingEnabled.Off():
			err = errors.New("torrent networking disabled")
			return
		case <-dontWait:
			return
		case <-readerCond:
		}
	}
}

// Adds the reader's torrent offset to the reader object offset (for example the reader might be
// constrainted to a particular file within the torrent).
func (r *reader) torrentOffset(readerPos int64) int64 {
	return r.offset + readerPos
}

// Performs at most one successful read to torrent storage.
func (r *reader) readOnceAt(ctx context.Context, b []byte, pos int64) (n int, err error) {
	if pos >= r.length {
		err = io.EOF
		return
	}
	for {
		var avail int64
		avail, err = r.waitAvailable(ctx, pos, int64(len(b)), n == 0)
		if avail == 0 {
			return
		}
		firstPieceIndex := pieceIndex(r.torrentOffset(pos) / r.t.info.PieceLength)
		firstPieceOffset := r.torrentOffset(pos) % r.t.info.PieceLength
		b1 := missinggo.LimitLen(b, avail)
		n, err = r.t.readAt(b1, r.torrentOffset(pos))
		if n != 0 {
			err = nil
			return
		}
		r.t.cl.lock()
		// TODO: Just reset pieces in the readahead window. This might help
		// prevent thrashing with small caches and file and piece priorities.
		r.log(log.Fstr("error reading torrent %s piece %d offset %d, %d bytes: %v",
			r.t.infoHash.HexString(), firstPieceIndex, firstPieceOffset, len(b1), err))
		if !r.t.updatePieceCompletion(firstPieceIndex) {
			r.log(log.Fstr("piece %d completion unchanged", firstPieceIndex))
		}
		// Update the rest of the piece completions in the readahead window, without alerting to
		// changes (since only the first piece, the one above, could have generated the read error
		// we're currently handling).
		if r.pieces.begin != firstPieceIndex {
			panic(fmt.Sprint(r.pieces.begin, firstPieceIndex))
		}
		for index := r.pieces.begin + 1; index < r.pieces.end; index++ {
			r.t.updatePieceCompletion(index)
		}
		r.t.cl.unlock()
	}
}

// Hodor
func (r *reader) Close() error {
	r.t.cl.lock()
	defer r.t.cl.unlock()
	r.t.deleteReader(r)
	return nil
}

func (r *reader) posChanged() {
	to := r.piecesUncached()
	from := r.pieces
	if to == from {
		return
	}
	r.pieces = to
	// log.Printf("reader pos changed %v->%v", from, to)
	r.t.readerPosChanged(from, to)
}

func (r *reader) Seek(off int64, whence int) (newPos int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	newPos, err = func() (int64, error) {
		switch whence {
		case io.SeekStart:
			return off, err
		case io.SeekCurrent:
			return r.pos + off, nil
		case io.SeekEnd:
			return r.length + off, nil
		default:
			return r.pos, errors.New("bad whence")
		}
	}()
	if err != nil {
		return
	}
	if newPos == r.pos {
		return
	}
	r.reading = false
	r.pos = newPos
	r.contiguousReadStartPos = newPos

	r.posChanged()
	return
}

func (r *reader) log(m log.Msg) {
	r.t.logger.Log(m.Skip(1))
}

// Implementation inspired by https://news.ycombinator.com/item?id=27019613.
func (r *reader) defaultReadaheadFunc() int64 {
	return r.pos - r.contiguousReadStartPos
}
