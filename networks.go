package torrent

import "strings"

var allPeerNetworks = func() (ret []network) {
	for _, s := range []string{"tcp4", "tcp6", "udp4", "udp6"} {
		ret = append(ret, parseNetworkString(s))
	}
	return
}()

type network struct {
	Ipv4 bool
	Ipv6 bool
	Udp  bool
	Tcp  bool
}

func (n network) String() (ret string) {
	switch {
	case n.Udp && n.Ipv4:
		return "udp4"
	case n.Tcp && n.Ipv4:
		return "tcp4"
	case n.Udp && n.Ipv6:
		return "udp6"
	case n.Tcp && n.Ipv6:
		return "tcp6"
	case n.Tcp:
		return "tcp"
	case n.Udp:
		return "udp"
	}
	panic("")
	return
}

func parseNetworkString(network string) (ret network) {
	c := func(s string) bool {
		return strings.Contains(network, s)
	}
	ret.Ipv4 = c("4")
	ret.Ipv6 = c("6")
	ret.Udp = c("udp")
	ret.Tcp = c("tcp")
	return
}

func peerNetworkEnabled(n network, cfg *ClientConfig) bool {
	if cfg.DisableUTP && n.Udp {
		return false
	}
	if cfg.DisableTCP && n.Tcp {
		return false
	}
	if cfg.DisableIPv6 && n.Ipv6 {
		return false
	}
	if cfg.DisableIPv4 && n.Ipv4 {
		return false
	}
	return true
}
