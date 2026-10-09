package core

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

//go:embed dashboard.html
var dashboardHTML []byte

func (w *Worker) StartAPIServer(port string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/status", w.handleGetStatus)

	mux.HandleFunc("/", func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html")
		res.Write(dashboardHTML)
	})

	handler := corsMiddleware(mux)

	fmt.Printf("[API] Local Node Dashboard listening on http://localhost:%s\n", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		fmt.Printf("[API] Fatal server error: %v\n", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (w *Worker) handleGetStatus(res http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(res, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	brokerAddr := os.Getenv("EMDASH_BROKER_ADDR")
	if brokerAddr == "" {
		brokerAddr = "localhost:8080"
	}

	res.Header().Set("Content-Type", "application/json")
	json.NewEncoder(res).Encode(map[string]interface{}{
		"node_id":        w.ID,
		"location":       fmt.Sprintf("%s, %s (%s)", w.Geo.City, w.Geo.Country, w.Geo.CountryCode),
		"public_ip":      w.Geo.IP,
		"isp":            w.Geo.ISP,
		"price_per_task": w.PricePerTask,
		"broker_addr":    brokerAddr,
	})
}
