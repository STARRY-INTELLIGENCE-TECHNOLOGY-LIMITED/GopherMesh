package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

func main() {
	name := flag.String("name", "stdio-echo", "Service name")
	flag.Parse()

	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatal(err)
	}

	if os.Getenv("GOPHERMESH_STDIO_MODE") == "http" {
		chunks := []string{
			fmt.Sprintf("[%s] streaming response from STDIO bridge\n", *name),
			fmt.Sprintf("method=%s uri=%s\n", os.Getenv("REQUEST_METHOD"), os.Getenv("REQUEST_URI")),
			"----- raw request begin -----\n",
			string(payload),
			"\n----- raw request end -----\n",
		}
		for i, chunk := range chunks {
			if _, err := io.WriteString(os.Stdout, chunk); err != nil {
				log.Fatal(err)
			}
			if i < len(chunks)-1 {
				time.Sleep(200 * time.Millisecond)
			}
		}
		return
	}

	if _, err := fmt.Fprintf(os.Stdout, "%s|%s", *name, string(payload)); err != nil {
		log.Fatal(err)
	}
}
