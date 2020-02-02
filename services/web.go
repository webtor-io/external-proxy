package services

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

type Web struct {
	host string
	port int
	ln   net.Listener
}

const (
	WEB_HOST_FLAG = "host"
	WEB_PORT_FLAG = "port"
)

func NewWeb(c *cli.Context) *Web {
	return &Web{host: c.String(WEB_HOST_FLAG), port: c.Int(WEB_PORT_FLAG)}
}

func RegisterWebFlags(c *cli.App) {
	c.Flags = append(c.Flags, cli.StringFlag{
		Name:  WEB_HOST_FLAG,
		Usage: "listening host",
		Value: "",
	})
	c.Flags = append(c.Flags, cli.IntFlag{
		Name:  WEB_PORT_FLAG,
		Usage: "http listening port",
		Value: 8080,
	})
}

func (s *Web) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrap(err, "Failed to web listen to tcp connection")
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) != 3 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			log.WithError(err).Error("Failed to decode")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		u, err := url.Parse(string(decoded))
		if err != nil {
			log.WithError(err).Error("Failed to parse url")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		resp, err := http.Get(u.String())
		if err != nil {
			log.WithError(err).Error("Failed to get url")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t := resp.Header.Get("Content-Type")
		if t != "" {
			w.Header().Add("Content-Type", t)
		}
		l, err := io.Copy(w, resp.Body)
		if err != nil {
			log.WithError(err).Error("Failed to fetch data")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		log.Infof("Serving url %v, %v bytes written", u, l)
	})
	log.Infof("Serving Web at %v", addr)
	return http.Serve(ln, mux)
}

func (s *Web) Close() {
	if s.ln != nil {
		s.ln.Close()
	}
}
