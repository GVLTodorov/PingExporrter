package main

import (
	"context"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const landingPage = `<html>
<head><title>ping_exporter</title></head>
<body>
<h1>ping_exporter</h1>
<p><a href="/metrics">Metrics</a></p>
</body>
</html>`

func main() {
	configPath := envOr("CONFIG_FILE", defaultConfigFile)
	listenAddr := envOr("LISTEN_ADDR", defaultListenAddr)

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	log.Printf(
		"Loaded %d target(s) from %s (interval=%v timeout=%v history-size=%d size=%d disableIPv6=%v)",
		len(cfg.Targets), configPath, cfg.Ping.Interval.Duration(), cfg.Ping.Timeout.Duration(),
		cfg.Ping.HistorySize, cfg.Ping.Size, cfg.Options.DisableIPv6,
	)

	registerMetrics()
	up.WithLabelValues(version).Set(1)

	eng, err := newICMPEngine(!cfg.Options.DisableIPv6)
	if err != nil {
		log.Fatalf("ICMP engine: %v", err)
	}
	defer eng.Close()

	mgr := newManager(cfg, eng)
	if err := mgr.Start(context.Background()); err != nil {
		log.Fatalf("Starting probers: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(landingPage))
	})

	log.Printf("Listening on %s", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, mux))
}
