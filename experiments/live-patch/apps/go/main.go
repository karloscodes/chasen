package main

import (
	"fmt"
	"log"
	"net/http"
	"runtime"

	"golang.org/x/net/html"
)

func main() {
	http.HandleFunc("/up", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Use the module so the linker keeps golang.org/x/net in the binary.
		path := html.EscapeString(r.URL.Path)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "go %s path %s\n", runtime.Version(), path)
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
