package main

import (
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	s "github.com/webtor-io/external-proxy/services"
)

func configure(app *cli.App) {
	app.Flags = []cli.Flag{}
	cs.RegisterProbeFlags(app)
	s.RegisterWebFlags(app)
	app.Action = run
}

func run(c *cli.Context) error {
	// Setting ProbeService
	probe := cs.NewProbe(c)
	defer probe.Close()

	// Setting WebService
	web := s.NewWeb(c)
	defer web.Close()

	// Setting ServeService
	serve := cs.NewServe(probe, web)

	// And SERVE!
	err := serve.Serve()
	if err != nil {
		log.WithError(err).Error("Got server error")
	}
	return err
}
