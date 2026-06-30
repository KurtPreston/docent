package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/kurt/slakkr-ai/apps/docentd/internal/config"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/engine"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/registry"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/server"
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

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	log.Printf("docentd serving on http://%s/", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
