package main

import (
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	s "github.com/webtor-io/external-proxy/services"
)

func configure(app *cli.App) {
	app.Flags = []cli.Flag{}
	app.Flags = cs.RegisterProbeFlags(app.Flags)
	app.Flags = s.RegisterWebFlags(app.Flags)
	app.Action = run
}

func run(c *cli.Context) error {
	var servers []cs.Servable
	// Setting ProbeService
	probe := cs.NewProbe(c)
	if probe != nil {
		servers = append(servers, probe)
		defer probe.Close()
	}

	// Setting WebService
	web := s.NewWeb(c)
	defer web.Close()
	servers = append(servers, web)

	// Setting ServeService
	serve := cs.NewServe(servers...)

	// And SERVE!
	err := serve.Serve()
	if err != nil {
		log.WithError(err).Error("got server error")
	}
	return err
}
