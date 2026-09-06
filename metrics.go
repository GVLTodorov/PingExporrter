package main

import "github.com/prometheus/client_golang/prometheus"

// Metric names, types and labels must match czerwonk/ping_exporter exactly
// (see PING_EXPORTER_REQUIREMENTS.md section 4) so existing Prometheus
// scrape configs and Grafana panels keep working unchanged.
var (
	lossRatio = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ping_loss_ratio",
		Help: "Fraction of probes lost in the current history window (0.0-1.0).",
	}, []string{"ip", "ip_version", "target"})

	rttBest = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ping_rtt_best_seconds",
		Help: "Minimum round-trip time in the current history window, in seconds.",
	}, []string{"ip", "ip_version", "target"})

	rttMean = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ping_rtt_mean_seconds",
		Help: "Mean round-trip time in the current history window, in seconds.",
	}, []string{"ip", "ip_version", "target"})

	rttStdDeviation = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ping_rtt_std_deviation_seconds",
		Help: "Standard deviation of round-trip time in the current history window, in seconds.",
	}, []string{"ip", "ip_version", "target"})

	rttWorst = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ping_rtt_worst_seconds",
		Help: "Maximum round-trip time in the current history window, in seconds.",
	}, []string{"ip", "ip_version", "target"})

	up = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ping_up",
		Help: "Always 1 while the exporter process is alive; version label carries the exporter's build version.",
	}, []string{"version"})
)

var allCollectors = []prometheus.Collector{
	lossRatio, rttBest, rttMean, rttStdDeviation, rttWorst, up,
}

func registerMetrics() {
	prometheus.MustRegister(allCollectors...)
}

// applyStats updates one target/IP's series from its latest window. The RTT
// gauges are still touched (set to 0) even with no successful probe yet, so
// all six metric families show one series per configured target/IP-version
// combination from startup, per the acceptance criteria — a target that's
// down shows loss_ratio=1 and rtt=0 rather than simply being absent.
func applyStats(target, ip, ipVersion string, st stats) {
	lossRatio.WithLabelValues(ip, ipVersion, target).Set(st.lossRatio)

	var best, mean, stddev, worst float64
	if st.haveRTT {
		best = st.best.Seconds()
		mean = st.mean.Seconds()
		stddev = st.stddev.Seconds()
		worst = st.worst.Seconds()
	}
	rttBest.WithLabelValues(ip, ipVersion, target).Set(best)
	rttMean.WithLabelValues(ip, ipVersion, target).Set(mean)
	rttStdDeviation.WithLabelValues(ip, ipVersion, target).Set(stddev)
	rttWorst.WithLabelValues(ip, ipVersion, target).Set(worst)
}

// removeSeries deletes one target/IP's series from every gauge, used when a
// hostname target's DNS refresh drops a previously-resolved IP so that IP
// stops appearing in /metrics instead of serving a stale last-known value.
func removeSeries(target, ip, ipVersion string) {
	lossRatio.DeleteLabelValues(ip, ipVersion, target)
	rttBest.DeleteLabelValues(ip, ipVersion, target)
	rttMean.DeleteLabelValues(ip, ipVersion, target)
	rttStdDeviation.DeleteLabelValues(ip, ipVersion, target)
	rttWorst.DeleteLabelValues(ip, ipVersion, target)
}
