package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "go") })
	_ = http.ListenAndServe(":"+port, nil)
}
