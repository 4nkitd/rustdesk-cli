package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

type entry struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Platform string   `json:"platform"`
	Tags     []string `json:"tags"`
}

var entries = []entry{{ID: "11", Name: "alpha", Platform: "linux", Tags: []string{"servers"}}}

func main() {
	http.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok"}`))
	})
	http.HandleFunc("/api/ab", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(entries)
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&entries)
			fmt.Fprintln(os.Stderr, "PUT", entries)
			_, _ = w.Write([]byte(`ok`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	addr := "127.0.0.1:18080"
	fmt.Println("api listening", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Println("listen:", err)
		os.Exit(1)
	}
}
