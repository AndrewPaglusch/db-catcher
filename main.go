// Command db-catcher receives database dumps over HTTP, checks that they are
// complete, and stores them compressed in S3-compatible object storage.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the config file")
	flag.Parse()
	if flag.Arg(0) == "healthcheck" {
		os.Exit(healthcheck(*configPath))
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	store, err := newS3Store(context.Background(), cfg.S3)
	if err != nil {
		log.Fatal(err)
	}

	a := &app{store: store, tracker: newTracker()}
	if err := a.tracker.sync(context.Background(), store, cfg.Backups); err != nil {
		log.Fatal(err)
	}
	a.config.Store(cfg)

	go func() {
		for now := range time.Tick(time.Minute) {
			a.checkOverdue(now)
		}
	}()

	server := &http.Server{Addr: cfg.Listen, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("listening on %s with %d backups configured", cfg.Listen, len(cfg.Backups))
		var err error
		if cfg.TLSCert != "" {
			err = server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			err = server.ListenAndServe()
		}
		if err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	for sig := range signals {
		if sig != syscall.SIGHUP {
			break
		}
		a.reload(*configPath)
	}

	log.Print("shutting down, waiting for uploads in progress to finish")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Print(err)
	}
}

// reload swaps in a new config. A config that fails to load is rejected and
// the current one stays in place. Uploads already running keep the config
// they started with.
func (a *app) reload(path string) {
	cfg, err := loadConfig(path)
	if err != nil {
		log.Printf("reload rejected, keeping current config: %v", err)
		return
	}
	if err := a.tracker.sync(context.Background(), a.store, cfg.Backups); err != nil {
		log.Printf("reload rejected, keeping current config: %v", err)
		return
	}

	old := a.config.Swap(cfg)
	if cfg.Listen != old.Listen || cfg.TLSCert != old.TLSCert || cfg.TLSKey != old.TLSKey || cfg.S3 != old.S3 {
		log.Print("reload: changes to listen, tls and s3 settings take effect after a restart")
	}
	log.Printf("config reloaded with %d backups", len(cfg.Backups))
}

// healthcheck exists for Docker's HEALTHCHECK, because the runtime image
// has no curl.
func healthcheck(configPath string) int {
	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Print(err)
		return 1
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		log.Print(err)
		return 1
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := "http://127.0.0.1:" + port + "/healthz"
	if cfg.TLSCert != "" {
		url = "https://127.0.0.1:" + port + "/healthz"
		// The certificate names the public hostname, not 127.0.0.1.
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}

	resp, err := client.Get(url)
	if err != nil {
		log.Print(err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("healthz returned %s", resp.Status)
		return 1
	}
	return 0
}
