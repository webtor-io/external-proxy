package services

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"
)

const (
	WEB_PRIVATE_NETWORK_ORIGINS_FLAG = "private-network-origins"

	// maxRemoteBodySize caps what one fetch may relay. Legitimate fetches are
	// posters and subtitles: the largest 2xx over 24 h of prod /ext/ traffic
	// was 2.4 MB (p99 1.9 MB), and a user subtitle upload is capped at 5 MB.
	maxRemoteBodySize = 20 << 20
)

// privateNetworkHint goes with every refusal that listing the origin would lift.
const privateNetworkHint = "if this origin is trusted (e.g. this instance's own web-ui), " +
	"list it in --" + WEB_PRIVATE_NETWORK_ORIGINS_FLAG + " / PRIVATE_NETWORK_ORIGINS"

// nonPublicPrefixes are the ranges netip has no predicate for that a fetch
// must not reach: special-purpose ranges that are not globally reachable
// (IANA special-purpose registries) and NAT64 prefixes, which embed an IPv4
// address a translator would dial on our behalf.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"; 0.x dials the local host on Linux
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space (CGNAT), used by CNIs
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, and the 255.255.255.255 broadcast
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("3fff::/20"),       // documentation
}

// isPublicAddr reports whether a fetch may connect to ip: a public unicast
// address, whatever notation it came in.
func isPublicAddr(ip netip.Addr) bool {
	// A zone would make Prefix.Contains false for any prefix.
	ip = ip.Unmap().WithZone("")
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// refusedError is a fetch declined by policy, as opposed to one that failed.
// Neither field carries the query string: it can hold the target's own
// credentials.
type refusedError struct {
	host   string
	reason string
}

func (e *refusedError) Error() string {
	return e.reason
}

// dialPublicOnly is a net.Dialer Control: it runs on the resolved address of
// every connection attempt, so it also covers each redirect hop and a name
// that resolves differently the second time.
func dialPublicOnly(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return &refusedError{reason: fmt.Sprintf("unparsable address %q", address)}
	}
	if !isPublicAddr(ap.Addr()) {
		return &refusedError{reason: fmt.Sprintf("%v is not a public address; %v", ap.Addr(), privateNetworkHint)}
	}
	return nil
}

// checkFetchURL holds for every URL a fetch goes to, the first and each
// redirect.
func checkFetchURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return &refusedError{host: u.Host, reason: fmt.Sprintf("scheme %q is not http or https", u.Scheme)}
	}
	// No caller puts credentials in the URL, and the client would send them
	// on as an Authorization header to whatever the URL names.
	if u.User != nil {
		return &refusedError{host: u.Host, reason: "url carries credentials"}
	}
	return nil
}

// originOf is u's scheme://host:port with the port spelled out, so
// http://Host and http://host:80 are one origin. url.Parse already lower-cases
// the scheme.
func originOf(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// parsePrivateNetworkOrigins turns the flag's entries into a set of origins.
// A path is ignored: what is trusted is the origin.
func parsePrivateNetworkOrigins(entries []string) (map[string]bool, error) {
	origins := map[string]bool{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		u, err := url.Parse(e)
		if err == nil && u.Host != "" {
			err = checkFetchURL(u)
		} else if err == nil {
			err = errors.New("no host")
		}
		if err != nil {
			return nil, errors.Wrapf(err, "%v %q must be scheme://host[:port]", WEB_PRIVATE_NETWORK_ORIGINS_FLAG, e)
		}
		origins[originOf(u)] = true
	}
	return origins, nil
}

func newRemoteTransport(control func(network, address string, c syscall.RawConn) error) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	// Through a proxy the dialer would only ever see the proxy's address.
	t.Proxy = nil
	t.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   control,
	}).DialContext
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 30 * time.Second
	return t
}

// newRemoteClients returns the client for public origins, which connects to
// public addresses only, and the one for the trusted private-network origins,
// which connects anywhere but redirects only among those origins.
func newRemoteClients(origins map[string]bool) (public *http.Client, private *http.Client) {
	public = &http.Client{
		Transport: newRemoteTransport(dialPublicOnly),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return checkFetchURL(req.URL)
		},
	}
	private = &http.Client{
		Transport: newRemoteTransport(nil),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if err := checkFetchURL(req.URL); err != nil {
				return err
			}
			if !origins[originOf(req.URL)] {
				return &refusedError{host: req.URL.Host, reason: "redirect from a private-network origin to one not listed; " + privateNetworkHint}
			}
			return nil
		},
	}
	return
}
