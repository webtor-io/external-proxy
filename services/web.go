package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"github.com/vincent-petithory/dataurl"
	cs "github.com/webtor-io/common-services"
)

type Web struct {
	host           string
	port           int
	gs             *cs.GracefulServer
	privateOrigins map[string]bool
	publicClient   *http.Client
	privateClient  *http.Client
}

const (
	WEB_HOST_FLAG = "host"
	WEB_PORT_FLAG = "port"
)

func NewWeb(c *cli.Context) (*Web, error) {
	entries := c.StringSlice(WEB_PRIVATE_NETWORK_ORIGINS_FLAG)
	origins, err := parsePrivateNetworkOrigins(entries)
	if err != nil {
		return nil, err
	}
	if len(origins) > 0 {
		log.WithField("origins", entries).Info("these origins are fetched even at private network addresses")
	}
	public, private := newRemoteClients(origins)
	return &Web{
		host:           c.String(WEB_HOST_FLAG),
		port:           c.Int(WEB_PORT_FLAG),
		gs:             cs.NewGracefulServer(cs.ShutdownTimeout(c)),
		privateOrigins: origins,
		publicClient:   public,
		privateClient:  private,
	}, nil
}

func RegisterWebFlags(f []cli.Flag) []cli.Flag {
	f = cs.RegisterShutdownFlags(f)
	return append(f,
		cli.StringFlag{
			Name:   WEB_HOST_FLAG,
			Usage:  "listening host",
			Value:  "",
			EnvVar: "WEB_HOST",
		},
		cli.IntFlag{
			Name:   WEB_PORT_FLAG,
			Usage:  "http listening port",
			Value:  8080,
			EnvVar: "WEB_PORT",
		},
		cli.StringSliceFlag{
			Name: WEB_PRIVATE_NETWORK_ORIGINS_FLAG,
			Usage: "origins (scheme://host[:port]) fetched even when they resolve to a private, loopback " +
				"or other non-public address, such as this instance's own web-ui; any other url is fetched " +
				"from public addresses only",
			EnvVar: "PRIVATE_NETWORK_ORIGINS",
		},
	)
}

func (s *Web) ServeRemote(ctx context.Context, data string) (io.ReadCloser, string, error) {
	u, err := url.Parse(data)
	if err != nil {
		// Without url.Error's copy of the input, query included.
		return nil, "", errors.Wrap(errors.Unwrap(err), "failed to parse url")
	}
	if err := checkFetchURL(u); err != nil {
		return nil, "", err
	}
	cl := s.publicClient
	if s.privateOrigins[originOf(u)] {
		cl = s.privateClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to make request")
	}
	resp, err := cl.Do(req)
	if err != nil {
		// url.Error repeats the url, query included.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		var re *refusedError
		if errors.As(err, &re) {
			if re.host == "" && ue != nil {
				if hu, perr := url.Parse(ue.URL); perr == nil {
					re.host = hu.Host
				}
			}
			return nil, "", re
		}
		return nil, "", errors.Wrapf(err, "failed to get url host=%v path=%v", u.Host, u.Path)
	}
	if resp.ContentLength > maxRemoteBodySize {
		_ = resp.Body.Close()
		return nil, "", &refusedError{
			host:   resp.Request.URL.Host,
			reason: fmt.Sprintf("content length %d is over the %d limit", resp.ContentLength, maxRemoteBodySize),
		}
	}
	t := resp.Header.Get("Content-Type")
	return resp.Body, t, nil
}

func (s *Web) ServeData(data string) (io.ReadCloser, string, error) {
	dataURL, err := dataurl.DecodeString(data)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to parse data url")
	}
	return ioutil.NopCloser(bytes.NewReader(dataURL.Data)), dataURL.ContentType(), nil
}

func (s *Web) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrap(err, "failed to web listen to tcp connection")
	}
	log.Infof("Serving Web at %v", addr)
	return s.serve(ln)
}

func (s *Web) serve(ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// /<base64>/<name>. Standard base64 has '/' in its alphabet (an ASCII
		// url gets one where '?' falls on a byte offset of 2 mod 3), and the
		// router hands it over unescaped, so only the last '/' ends the payload.
		p := strings.TrimPrefix(r.URL.Path, "/")
		i := strings.LastIndex(p, "/")
		if i <= 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(p[:i])
		if err != nil {
			log.WithError(err).Error("failed to decode")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data := string(decoded)
		ct := ""
		var rc io.ReadCloser
		if strings.HasPrefix(data, "http") {
			rc, ct, err = s.ServeRemote(r.Context(), data)
		} else if strings.HasPrefix(data, "data") {
			rc, ct, err = s.ServeData(data)
		} else {
			err = errors.New("payload is neither an http(s) nor a data url")
		}
		if err != nil {
			// Same answer as any other failure: a distinct status would tell
			// a prober which internal names resolve.
			var re *refusedError
			if errors.As(err, &re) {
				log.WithFields(log.Fields{
					"host":   re.host,
					"reason": re.reason,
				}).Warn("refused to fetch")
			} else {
				log.WithError(err).Error("failed to process data")
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer rc.Close()
		if ct != "" {
			w.Header().Add("Content-Type", ct)
		}
		_, err = io.CopyN(w, rc, maxRemoteBodySize)
		if err == nil {
			// The cap was reached: whatever is left is cut.
			if n, _ := io.ReadFull(rc, make([]byte, 1)); n > 0 {
				host := ""
				if u, perr := url.Parse(data); perr == nil {
					host = u.Host
				}
				log.WithFields(log.Fields{
					"host":  host,
					"limit": maxRemoteBodySize,
				}).Warn("response cut at the size limit")
			}
		} else if err != io.EOF {
			log.WithError(err).Error("failed to write response")
		}
	})
	srv := &http.Server{
		Handler: mux,
		// ReadTimeout:    5 * time.Minute,
		// WriteTimeout:   5 * time.Minute,
		MaxHeaderBytes: 50 << 20,
	}
	return s.gs.Serve(srv, ln)
}

// Close stops accepting and lets in-flight responses finish, up to
// WEB_SHUTDOWN_TIMEOUT. Closing only the listener let the process exit in the
// middle of every response. run() defers it last, so it runs first.
func (s *Web) Close() {
	s.gs.Close()
}
