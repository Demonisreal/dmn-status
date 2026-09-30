package check

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"syscall"
)

var errBlocked = errors.New("ziel gesperrt")

var (
	cgnat  = netip.MustParsePrefix("100.64.0.0/10")
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4 = netip.MustParsePrefix("2002::/16")

	blocked = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("::ffff:0:0:0/96"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("fec0::/10"),

		netip.MustParsePrefix("64:ff9b:1::/48"),
	}
)

func dialer(loopback bool, extra, block []netip.Addr) *net.Dialer {
	return &net.Dialer{
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !allowed(ap.Addr(), loopback, extra, block) {
				return errBlocked
			}
			return nil
		},
	}
}

func (c Checker) dialConnectTo(ctx context.Context, network, connectTo string) (net.Conn, error) {
	if !slices.ContainsFunc(c.PrivateAllow, func(s string) bool { return strings.EqualFold(s, connectTo) }) {
		return dialer(c.loopback, nil, c.Block).DialContext(ctx, network, connectTo)
	}
	host, port, err := net.SplitHostPort(connectTo)
	if err != nil {
		return nil, errBlocked
	}
	lookup := c.lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	for i, ip := range ips {
		ips[i] = ip.WithZone("").Unmap()
	}

	d := dialer(c.loopback, ips, c.Block)
	err = errBlocked
	for _, ip := range ips {
		var conn net.Conn
		conn, err = d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}

func allowed(ip netip.Addr, loopback bool, extra, block []netip.Addr) bool {
	ip = ip.WithZone("").Unmap()
	orig := ip
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		ip = netip.AddrFrom4([4]byte(b[12:]))
	case sixTo4.Contains(ip):
		ip = netip.AddrFrom4([4]byte(b[2:6]))
	}

	if slices.Contains(block, orig) || slices.Contains(block, ip) {
		return false
	}

	switch {
	case !ip.IsValid(), ip.IsUnspecified(), ip.IsLinkLocalUnicast(), ip.IsMulticast():
		return false
	case ip.IsLoopback():
		return loopback
	case ip.IsPrivate(), cgnat.Contains(ip):
		return slices.Contains(extra, orig)
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
