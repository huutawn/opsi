package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	apiEndpoint := os.Getenv("API_ENDPOINT")
	if apiEndpoint == "" {
		apiEndpoint = os.Getenv("WORKER_API_URL")
	}
	if apiEndpoint == "" {
		apiEndpoint = "http://127.0.0.1:8080"
	}

	var proxy *httputil.ReverseProxy
	if parsed, err := url.Parse(apiEndpoint); err == nil {
		proxy = httputil.NewSingleHostReverseProxy(parsed)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if proxy != nil {
			originalPath := r.URL.Path
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
			if r.URL.Path == "" {
				r.URL.Path = "/"
			}
			proxy.ServeHTTP(w, r)
			r.URL.Path = originalPath
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","backend":"same_origin"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `<!DOCTYPE html>
<html>
<head><title>Opsi Web App</title></head>
<body>
  <h1>Opsi Web Service</h1>
  <div id="status">ready</div>
  <script>
    fetch("/api/health")
      .then(res => res.text())
      .then(data => {
        document.getElementById("status").textContent = "api-ok: " + data;
      })
      .catch(err => {
        document.getElementById("status").textContent = "api-err: " + err;
      });
  </script>
</body>
</html>`)
	})

	log.Printf("ADC-06 web listening on :%s (proxying /api to %s)", port, apiEndpoint)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
