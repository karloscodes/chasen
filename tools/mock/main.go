// The mock Chasen server, for development. It answers the protocol with
// made-up apps and runs nothing. bin/dev starts it.
//
//	go run ./tools/mock [address]     the default address is 127.0.0.1:4777
//
// Then point chasen at it: CHASEN_URL=http://127.0.0.1:4777 CHASEN_TOKEN=dev
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/karloscodes/chasen/internal/mock"
)

func main() {
	address := "127.0.0.1:4777"
	if len(os.Args) > 1 {
		address = os.Args[1]
	}
	server := mock.New(time.Now())
	server.Log = log.New(os.Stdout, "", log.Ltime)
	log.Printf("The mock server listens on http://%s. Its token is %q.", address, server.Token)
	log.Fatal(http.ListenAndServe(address, server))
}
