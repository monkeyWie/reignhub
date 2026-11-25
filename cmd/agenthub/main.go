// Legacy MyGo UI. The maintained desktop app is clients/nsgo.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/monkeyWie/reignhub/internal/app"
	"github.com/monkeyWie/reignhub/internal/codex"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/desktop"
)

func main() {
	if codex.RunSharedWorker() {
		return
	}
	dir := flag.String("data-dir", config.DefaultDir(), "private configuration directory")
	flag.Parse()
	c, err := config.Load(*dir)
	if err != nil {
		log.Fatal(err)
	}
	key, err := config.AccessKey(c.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	embedded, err := app.StartEmbedded(context.Background(), c)
	if err != nil {
		log.Fatal(err)
	}
	defer embedded.Close()
	if err = desktop.Run(c, key, embedded); err != nil {
		log.Print(err)
	}
}
