package homelab

import (
	"fmt"
	"net/http"
)

func main() {
	config := LoadServerConfig()
	fmt.Println(config)

	// ping server
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Welcome to my website!")
	})

	// todo - health checks

	http.ListenAndServe(":3000", nil)
}
