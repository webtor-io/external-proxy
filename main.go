package main

import (
	"os"

	joonix "github.com/joonix/log"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

func main() {
	log.SetFormatter(joonix.NewFormatter())
	app := cli.NewApp()
	app.Name = "external-proxy"
	app.Usage = "Proxies external urls"
	app.Version = "0.0.1"
	configure(app)
	err := app.Run(os.Args)
	if err != nil {
		log.WithError(err).Fatal("Failed to serve application")
	}
}
