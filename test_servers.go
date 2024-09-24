package main

import (
	"fmt"
	"log"
	"net/http"
)

var Ports = []int{3031, 3032, 3033, 3034}

func StartServers(ready chan bool) {
	for _, port := range Ports {
		go func(port int) {
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "Hello from server on port %d", port)
			})
			log.Printf("Starting server on port %d\n", port)
			if err := http.ListenAndServe(fmt.Sprintf(":%d", port), mux); err != nil {
				log.Fatalf("Server on port %d failed: %v", port, err)
			}
		}(port)
	}
	ready <- true
}
