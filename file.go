package torrent

import (
	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/missinggo/v2/bitmap"

	"github.com/anacrolix/torrent/metainfo"
)

// Provides access to regions of torrent data that correspond to its files.
type File struct {
	t           *Torrent
	path        string
	offset      int64
	length      int64
	fi          metainfo.FileInfo
	displayPath string
	prio        piecePriority
}

func (f *File) Torrent() *Torrent {
	return f.t
}

// Data for this file begins this many bytes into the Torrent.
func (f *File) Offset() int64 {
	return f.offset
}

// The FileInfo from the metainfo.Info to which this file corresponds.
func (f File) FileInfo() metainfo.FileInfo {
	return f.fi
}

// The file's path components joined by '/'.
func (f File) Path() string {
	return f.path
}

// The file's length in bytes.
func (f *File) Length() int64 {
	return f.length
}

// Number of bytes of the entire file we have completed. This is the sum of
// completed pieces, and dirtied chunks of incomplete pieces.
func (f *File) BytesCompleted() int64 {
	f.t.cl.rLock()
	defer f.t.cl.rUnlock()
	return f.bytesCompleted()
}

func (f *File) bytesCompleted() int64 {
	return f.length - f.bytesLeft()
}

func fileBytesLeft(
	torrentUsualPieceSize int64,
	fileFirstPieceIndex int,
	fileEndPieceIndex int,
	fileTorrentOffset int64,
	fileLength int64,
	torrentCompletedPieces *roaring.Bitmap,
) (left int64) {
	numPiecesSpanned := fileEndPieceIndex - fileFirstPieceIndex
	switch numPiecesSpanned {
	case 0:
	case 1:
		if !torrentCompletedPieces.Contains(bitmap.BitIndex(fileFirstPieceIndex)) {
			left += fileLength
		}
	default:
		if !torrentCompletedPieces.Contains(bitmap.BitIndex(fileFirstPieceIndex)) {
			left += torrentUsualPieceSize - (fileTorrentOffset % torrentUsualPieceSize)
		}
		if !torrentCompletedPieces.Contains(bitmap.BitIndex(fileEndPieceIndex - 1)) {
			left += fileTorrentOffset + fileLength - int64(fileEndPieceIndex-1)*torrentUsualPieceSize
		}
		completedMiddlePieces := torrentCompletedPieces.Clone()
		completedMiddlePieces.RemoveRange(0, bitmap.BitRange(fileFirstPieceIndex+1))
		completedMiddlePieces.RemoveRange(bitmap.BitRange(fileEndPieceIndex-1), bitmap.ToEnd)
		left += int64(numPiecesSpanned-2-pieceIndex(completedMiddlePieces.GetCardinality())) * torrentUsualPieceSize
	}
	return
}

func (f *File) bytesLeft() (left int64) {
	return fileBytesLeft(int64(f.t.usualPieceSize()), f.firstPieceIndex(), f.endPieceIndex(), f.offset, f.length, &f.t._completedPieces)
}

// The relative file path for a multi-file torrent, and the torrent name for a
// single-file torrent. Dir separators are '/'.
func (f *File) DisplayPath() string {
	return f.displayPath
}

// The download status of a piece that comprises part of a File.
type FilePieceState struct {
	Bytes int64 // Bytes within the piece that are part of this File.
	PieceState
}

// Returns the state of pieces in this file.
func (f *File) State() []FilePieceState {
	f.t.cl.rLock()
	defer f.t.cl.rUnlock()
	if f.length == 0 {
		return nil
	}
	pieceSize := int64(f.t.usualPieceSize())
	startPieceIdx := pieceIndex(f.offset / pieceSize)
	startPieceOffset := f.offset % pieceSize
	if startPieceOffset + f.length <= pieceSize {
		return []FilePieceState{{f.length, f.t.pieceState(startPieceIdx)}}
	}

	endPieceIdx := pieceIndex((f.offset+f.length-1) / pieceSize)
	endPieceOffset := (f.offset+f.length-1) % pieceSize

	if endPieceIdx-startPieceIdx+1 < 2 { // invariant check for BCE
		panic("")
	}
	ret := make([]FilePieceState, endPieceIdx-startPieceIdx+1)
	ret[0] = FilePieceState{pieceSize - startPieceOffset, f.t.pieceState(startPieceIdx)}
	for i := 1; i < len(ret)-1; i++ {
		ret[i] = FilePieceState{pieceSize, f.t.pieceState(startPieceIdx+i)}
	}
	ret[len(ret)-1] = FilePieceState{endPieceOffset+1, f.t.pieceState(endPieceIdx)}
	return ret
}

// Requests that all pieces containing data in the file be downloaded.
func (f *File) Download() {
	f.SetPriority(PiecePriorityNormal)
}

func byteRegionExclusivePieces(off, size, pieceSize int64) (begin, end int) {
	begin = int((off + pieceSize - 1) / pieceSize)
	end = int((off + size) / pieceSize)
	return
}

// Deprecated: Use File.SetPriority.
func (f *File) Cancel() {
	f.SetPriority(PiecePriorityNone)
}

func (f *File) NewReader() Reader {
	tr := reader{
		mu:        f.t.cl.locker(),
		t:         f.t,
		readahead: 5 * 1024 * 1024,
		offset:    f.Offset(),
		length:    f.Length(),
	}
	f.t.addReader(&tr)
	return &tr
}

// Sets the minimum priority for pieces in the File.
func (f *File) SetPriority(prio piecePriority) {
	f.t.cl.lock()
	defer f.t.cl.unlock()
	if prio == f.prio {
		return
	}
	f.prio = prio
	f.t.updatePiecePriorities(f.firstPieceIndex(), f.endPieceIndex())
}

// Returns the priority per File.SetPriority.
func (f *File) Priority() piecePriority {
	f.t.cl.lock()
	defer f.t.cl.unlock()
	return f.prio
}

// Returns the index of the first piece containing data for the file.
func (f *File) firstPieceIndex() pieceIndex {
	if f.t.usualPieceSize() == 0 {
		return 0
	}
	return pieceIndex(f.offset / int64(f.t.usualPieceSize()))
}

// Returns the index of the piece after the last one containing data for the file.
func (f *File) endPieceIndex() pieceIndex {
	if f.t.usualPieceSize() == 0 {
		return 0
	}
	return pieceIndex((f.offset + f.length + int64(f.t.usualPieceSize()) - 1) / int64(f.t.usualPieceSize()))
}
