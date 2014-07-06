package spdystream

import (
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
)

const (
	LISTEN_ADDRESS = "127.0.0.1:7777"
)

func configureServer() (io.Closer, *sync.WaitGroup) {
	authenticated = true
	wg := &sync.WaitGroup{}
	server, serverErr := runServer(LISTEN_ADDRESS, wg)

	if serverErr != nil {
		panic(serverErr)
	}

	return server, wg
}

func BenchmarkDial10000(b *testing.B) {
	server, wg := configureServer()

	defer func() {
		server.Close()
		wg.Wait()
	}()

	for i := 0; i < b.N; i++ {
		conn, dialErr := net.Dial("tcp", LISTEN_ADDRESS)
		if dialErr != nil {
			panic(fmt.Sprintf("Error dialing server: %s", dialErr))
		}
		conn.Close()
	}
}

func BenchmarkDialWithSPDYStream10000(b *testing.B) {
	server, wg := configureServer()

	defer func() {
		server.Close()
		wg.Wait()
	}()

	for i := 0; i < b.N; i++ {
		conn, dialErr := net.Dial("tcp", LISTEN_ADDRESS)
		if dialErr != nil {
			b.Fatalf("Error dialing server: %s", dialErr)
		}

		spdyConn, spdyErr := NewConnection(conn, false)
		if spdyErr != nil {
			b.Fatalf("Error creating spdy connection: %s", spdyErr)
		}
		go spdyConn.Serve(NoOpStreamHandler)

		closeErr := spdyConn.Close()
		if closeErr != nil {
			b.Fatalf("Error closing connection: %s, closeErr")
		}
	}
}
