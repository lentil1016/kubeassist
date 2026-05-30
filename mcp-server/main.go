package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "kubeassist-mcp-server")
	})

	log.Println("MCP Server listening on :3000")
	log.Fatal(http.ListenAndServe(":3000", nil))
}
