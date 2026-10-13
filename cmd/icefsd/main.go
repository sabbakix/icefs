// Command icefsd is the IceFs service. It freezes finished Veeam backup files
// with the Linux immutable flag and melts them when retention ends.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sabbakix/icefs/internal/api"
	"github.com/sabbakix/icefs/internal/config"
	"github.com/sabbakix/icefs/internal/engine"
	"github.com/sabbakix/icefs/internal/store"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to config.toml")
	once := flag.Bool("once", false, "run a single scan and exit")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if os.Geteuid() != 0 {
		log.Warn("icefsd is not running as root; setting the immutable flag will fail")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("open state database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	eng := engine.New(cfg, *cfgPath, st, log)
	if *once {
		eng.Scan()
		return
	}

	ln, err := api.Listen(cfg.SocketPath)
	if err != nil {
		log.Error("open socket", "path", cfg.SocketPath, "err", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: api.New(eng).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("api server", "err", err)
		}
	}()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		eng.Run(stop)
		close(done)
	}()
	log.Info("icefsd started", "shares", len(cfg.Shares), "socket", cfg.SocketPath)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("shutting down")
	close(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	<-done
}
