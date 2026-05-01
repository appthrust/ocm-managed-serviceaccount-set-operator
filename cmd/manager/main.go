package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

func main() {
	var metricsAddr string
	var healthAddr string
	var leaderElect bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&healthAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for controller manager.")
	flag.Parse()

	_ = leaderElect

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("/readyz", ok)

	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", ok)

	go func() {
		if err := http.ListenAndServe(metricsAddr, metricsMux); err != nil {
			log.Fatalf("metrics server failed: %v", err)
		}
	}()

	fmt.Println("ocm-managed-serviceaccount-set-operator scaffold manager started")
	if err := http.ListenAndServe(healthAddr, mux); err != nil {
		log.Fatalf("health server failed: %v", err)
	}
}

func ok(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
