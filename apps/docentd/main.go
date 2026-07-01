package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/engine"
	"github.com/KurtPreston/docent/apps/docentd/internal/registry"
	"github.com/KurtPreston/docent/apps/docentd/internal/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		runDoctor(os.Args[2:])
		return
	}
	serve(os.Args[1:])
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "docentd config path")
	webRoot := fs.String("web", "apps/docentd/web", "dashboard static files")
	port := fs.Int("port", 0, "listen port override")
	host := fs.String("host", "", "bind address override (default 0.0.0.0 when a token is set, else 127.0.0.1)")
	_ = fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if *port > 0 {
		cfg.Port = *port
	}
	cfg.Directives = engine.EnsureDirectives(cfg.Directives)

	reg, err := registry.NewStore(cfg.RegistryPath)
	if err != nil {
		log.Fatal(err)
	}
	eng := engine.New(cfg, reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.StartScheduler(ctx)
	srv := server.New(cfg, eng, reg, *webRoot)

	bindHost := config.ResolveBindHost(cfg, *host)
	addr := net.JoinHostPort(bindHost, strconv.Itoa(cfg.Port))
	authState := "off"
	if cfg.Token != "" {
		authState = "on"
	}
	if !config.IsLoopbackHost(bindHost) && cfg.Token == "" {
		log.Printf("WARNING: docentd is bound to %s with NO token set — data endpoints are exposed unauthenticated. Set a token (DOCENT_TOKEN or token: in docentd.yaml) or bind 127.0.0.1.", bindHost)
	}
	log.Printf("docentd serving on http://%s/ (auth: %s)", addr, authState)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
