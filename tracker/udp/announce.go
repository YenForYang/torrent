package udp

import (
	"encoding"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent/tracker/shared"
)

// Marshalled as binary by the UDP client, so be careful making changes.
// See https://www.libtorrent.org/udp_tracker_protocol.html
type AnnounceRequest struct {
	InfoHash   [20]byte
	PeerId     [20]byte
	Downloaded int64
	Left       int64 // If less than 0, math.MaxInt64 will be used for HTTP trackers instead.
	Uploaded   int64
	// Apparently this is optional. AnnounceEventNone can be used for announces done at
	// regular intervals.
	Event     shared.AnnounceEvent
	IPAddress uint32
	Key       int32
	NumWant   int32 // How many peer addresses are desired. -1 for default.
	Port      uint16
} // 82 bytes + 6 bytes padding

type AnnounceResponsePeers interface {
	encoding.BinaryUnmarshaler
	NodeAddrs() []krpc.NodeAddr
}
