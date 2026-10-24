package services

import (
	"encoding/base64"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/urfave/cli"
)

func TestIsPublicAddr(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		public bool
	}{
		{"127.0.0.1", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"10.96.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"100.64.1.1", false},
		{"::ffff:100.64.1.1", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"192.0.0.8", false},
		{"192.0.2.1", false},
		{"198.18.0.1", false},
		{"198.51.100.1", false},
		{"203.0.113.1", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		{"224.0.0.1", false},
		{"fd00::1", false},
		{"fe80::1", false},
		{"fe80::1%eth0", false},
		{"ff02::1", false},
		{"::", false},
		{"64:ff9b::a00:1", false},
		{"64:ff9b::a00:1%eth0", false},
		{"64:ff9b:1::1", false},
		{"100::1", false},
		{"2001:db8::1", false},
		{"3fff::1", false},
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"172.66.40.137", true},
		{"2606:4700:4700::1111", true},
	} {
		if got := isPublicAddr(netip.MustParseAddr(tc.addr)); got != tc.public {
			t.Errorf("isPublicAddr(%v) = %v, want %v", tc.addr, got, tc.public)
		}
	}
}

func TestParsePrivateNetworkOrigins(t *testing.T) {
	got, err := parsePrivateNetworkOrigins([]string{"", " HTTP://LocalHost/ ", "https://webtor.example:8443/sub/path"})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://localhost:80/x", "https://webtor.example:8443/"} {
		pu, _ := url.Parse(u)
		if !got[originOf(pu)] {
			t.Errorf("%v not matched by %v", u, got)
		}
	}
	for _, bad := range []string{"localhost:8080", "ftp://host", "http://user@host", "http://"} {
		if _, err := parsePrivateNetworkOrigins([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// newTestWeb builds the Web the way the binary does, over RegisterWebFlags.
func newTestWeb(t *testing.T, privateOrigins ...string) *Web {
	t.Helper()
	set := flag.NewFlagSet("web", flag.ContinueOnError)
	for _, fl := range RegisterWebFlags(nil) {
		fl.Apply(set)
	}
	for _, o := range privateOrigins {
		if err := set.Set(WEB_PRIVATE_NETWORK_ORIGINS_FLAG, o); err != nil {
			t.Fatal(err)
		}
	}
	web, err := NewWeb(cli.NewContext(nil, set, nil))
	if err != nil {
		t.Fatal(err)
	}
	return web
}

// fetch asks a Web with these private-network origins for target through
// /<base64>/<name>, the way torrent-http-proxy hands it over (path unescaped).
func fetch(t *testing.T, target string, privateOrigins ...string) (int, string) {
	t.Helper()
	web := newTestWeb(t, privateOrigins...)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = web.serve(ln) }()
	defer web.Close()
	p := base64.StdEncoding.EncodeToString([]byte(target))
	resp, err := http.Get("http://" + ln.Addr().String() + "/" + p + "/name")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func countingServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// A loopback source is refused unless its origin is listed, by address and
// by name, and before any byte reaches it.
func TestRemoteNonPublicRefusedUnlessListed(t *testing.T) {
	src, hits := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "internal")
	})
	byName := strings.Replace(src.URL, "127.0.0.1", "localhost", 1)
	for _, u := range []string{src.URL + "/a.vtt", byName + "/a.vtt"} {
		if status, body := fetch(t, u); status != http.StatusNotFound || body != "" {
			t.Errorf("%v: got %d %q, want 404 and no body", u, status, body)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("refused source was reached %d times", n)
	}
	if status, body := fetch(t, src.URL+"/a.vtt", src.URL); status != http.StatusOK || body != "internal" {
		t.Fatalf("listed origin: got %d %q", status, body)
	}
}

func TestRemoteCredentialsRefused(t *testing.T) {
	src, hits := countingServer(t, func(w http.ResponseWriter, r *http.Request) {})
	withUser := strings.Replace(src.URL, "http://", "http://user:pass@", 1)
	if status, _ := fetch(t, withUser+"/a.vtt", src.URL); status != http.StatusNotFound || hits.Load() != 0 {
		t.Fatalf("url with credentials: got %d, %d hits", status, hits.Load())
	}
}

// A listed origin redirects only to listed origins.
func TestRemotePrivateRedirectStaysAmongListed(t *testing.T) {
	other, otherHits := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "other")
	})
	src, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/b.vtt", http.StatusFound)
	})
	if status, body := fetch(t, src.URL+"/a.vtt", src.URL); status != http.StatusNotFound || otherHits.Load() != 0 {
		t.Fatalf("redirect to unlisted origin: got %d %q, %d hits", status, body, otherHits.Load())
	}
	if status, body := fetch(t, src.URL+"/a.vtt", src.URL, other.URL); status != http.StatusOK || body != "other" {
		t.Fatalf("redirect to listed origin: got %d %q", status, body)
	}
}

func TestRemoteBodyLimit(t *testing.T) {
	big := strings.Repeat("x", maxRemoteBodySize+1)
	src, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/declared" {
			w.Header().Set("Content-Length", "21000000")
		}
		_, _ = io.WriteString(w, big)
	})
	if status, body := fetch(t, src.URL+"/declared", src.URL); status != http.StatusNotFound || body != "" {
		t.Errorf("declared over the limit: got %d and %d bytes, want 404 and none", status, len(body))
	}
	if status, body := fetch(t, src.URL+"/chunked", src.URL); status != http.StatusOK || len(body) != maxRemoteBodySize {
		t.Errorf("undeclared over the limit: got %d and %d bytes, want 200 and %d", status, len(body), maxRemoteBodySize)
	}
}

// A payload that is neither http(s) nor data (embed settings carry relative
// poster paths) is answered 404; it was a nil Close and a panic.
func TestRelativePayloadIs404(t *testing.T) {
	if status, _ := fetch(t, "/pictures/a.jpg"); status != http.StatusNotFound {
		t.Fatalf("got %d", status)
	}
}

// Base64 of a url with '?' at a byte offset of 2 mod 3 contains '/'.
func TestPayloadWithSlash(t *testing.T) {
	src, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "poster")
	})
	u := src.URL + "/p.jpg?v=1"
	for !strings.Contains(base64.StdEncoding.EncodeToString([]byte(u)), "/") {
		u = strings.Replace(u, "/p", "/pp", 1)
	}
	if status, body := fetch(t, u, src.URL); status != http.StatusOK || body != "poster" {
		t.Fatalf("got %d %q", status, body)
	}
}
