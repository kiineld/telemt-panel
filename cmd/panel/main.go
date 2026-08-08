package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/poller"
	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "proxies"), 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "panel.db"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	rt, err := docker.NewDockerRuntime()
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	// Bind mounts are resolved by the Docker daemon on the host, so the panel
	// needs the host's view of its data directory, not the container's.
	hostDataDir := os.Getenv("PANEL_HOST_DATA_DIR")
	if hostDataDir == "" {
		hostDataDir = cfg.DataDir
	}
	// ValidateHostDataDir catches both the plain-unset case above and the
	// quieter one where PANEL_HOST_DATA_DIR is non-empty but still wrong —
	// e.g. compose's ${PWD}/data collapsing to the container's own /data
	// when $PWD is unset (see its doc comment and Finding 6).
	if warn := config.ValidateHostDataDir(hostDataDir, cfg.DataDir); warn != "" {
		log.Printf("warning: %s", warn)
	}

	svc := proxy.New(proxy.Deps{
		Store: st, Runtime: rt, Cfg: cfg, HostDataDir: hostDataDir,
	})
	auth := web.NewAuth(st)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if pw, err := auth.Bootstrap(ctx); err != nil {
		log.Fatalf("bootstrap: %v", err)
	} else if pw != "" {
		log.Printf("=====================================================")
		log.Printf("  first-boot admin password: %s", pw)
		log.Printf("  username: admin — you will be asked to change this")
		log.Printf("=====================================================")
	}

	if err := rt.EnsureNetwork(ctx, cfg.Network, cfg.NetworkSubnet); err != nil {
		log.Printf("warning: ensure network: %v", err)
	}

	// Pre-pull the configured telemt image in the background so the panel is
	// reachable immediately and the first proxy creation does not stall for
	// however long the pull takes. context.WithoutCancel detaches the pull
	// from the shutdown signal above: an operator hitting Ctrl-C mid-pull
	// should not tear down a download that Docker itself will keep running on
	// the daemon side regardless.
	go func() {
		if err := rt.Pull(context.WithoutCancel(ctx), cfg.TelemtImage); err != nil {
			log.Printf("warning: pull %s: %v", cfg.TelemtImage, err)
			return
		}
		log.Printf("telemt image %s ready", cfg.TelemtImage)
	}()

	if rep, err := svc.Reconcile(ctx); err != nil {
		log.Printf("warning: reconcile: %v", err)
	} else {
		log.Printf("reconcile: %d restarted, %d cleaned up, %d orphans",
			len(rep.Restarted), len(rep.CleanedUp), len(rep.Orphans))
	}

	pl := poller.New(svc, cfg.PollInterval)
	go pl.Run(ctx)

	h, err := web.NewServer(web.ServerDeps{Auth: auth, Proxy: svc, Poller: pl, Cfg: cfg})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr, Handler: h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("panel listening on %s", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
