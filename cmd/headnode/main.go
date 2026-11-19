package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request){
		fmt.Fprintln(w, "Scheme:", r.Method)
		fmt.Fprintln(w, "Host:", r.URL)
		fmt.Fprintln(w, "Request Host:", r.Header)
		fmt.Fprintln(w, "Path:", r.Body)
		fmt.Fprintln(w, "RawQuery:", r.Host)
		fmt.Fprintln(w, "Fragment:", r.RemoteAddr)
	})

	log.Fatal(http.ListenAndServe("127.0.0.1:8080", nil))

}

