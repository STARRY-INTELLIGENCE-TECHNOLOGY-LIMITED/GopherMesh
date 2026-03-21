package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
)

func main() {
	port := flag.String("port", "19092", "Listen port")
	name := flag.String("name", "tcp-uppercase", "Service name")
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

			scanner := bufio.NewScanner(c)
			for scanner.Scan() {
				line := scanner.Text()
				_, _ = fmt.Fprintf(c, "%s|%s\n", *name, strings.ToUpper(line))
			}
		}(conn)
	}
}
