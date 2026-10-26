// healthcheck is a shell-free HTTP probe for the nonroot application images.
package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(1)
	}
	c := &http.Client{Timeout: 2 * time.Second}
	r, err := c.Get(os.Args[1])
	if err != nil {
		os.Exit(1)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
