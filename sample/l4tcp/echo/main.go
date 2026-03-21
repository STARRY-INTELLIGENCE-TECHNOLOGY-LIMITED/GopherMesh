package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
)

func main() {
	port := flag.String("port", "19091", "Listen port")
	name := flag.String("name", "tcp-echo", "Service name")
	flag.Parse()

	addr := "127.0.0.1:" + *port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()

	log.Printf("[%s] listening on %s", *name, addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[%s] accept error: %v", *name, err)
			return
		}

		go func(c net.Conn) {
			defer c.Close()
			payload, _ := io.ReadAll(c)
			_, _ = fmt.Fprintf(c, "%s|%s", *name, string(payload))
		}(conn)
	}
}
