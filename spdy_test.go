package spdystream

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestSpdyStreams(t *testing.T) {
	var wg sync.WaitGroup
	listen := "localhost:7443"
	server, serverErr := runServer(listen, &wg)
	if serverErr != nil {
		t.Fatalf("Error initializing server: %s", serverErr)
	}

	conn, dialErr := net.Dial("tcp", listen)
	if dialErr != nil {
		t.Fatalf("Error dialing server: %s", dialErr)
	}

	spdyConn, spdyErr := NewConnection(conn, false)
	if spdyErr != nil {
		t.Fatalf("Error creating spdy connection: %s", spdyErr)
	}
	go spdyConn.Serve(NoOpStreamHandler, RejectAuthHandler)

	authenticated = true
	stream, streamErr := spdyConn.CreateStream(http.Header{}, nil, false)
	if streamErr != nil {
		t.Fatalf("Error creating stream: %s", streamErr)
	}

	waitErr := stream.Wait()
	if waitErr != nil {
		t.Fatalf("Error waiting for stream: %s", waitErr)
	}

	message := []byte("hello")
	writeErr := stream.WriteData(message, false)
	if writeErr != nil {
		t.Fatalf("Error writing data")
	}

	buf := make([]byte, 10)
	n, readErr := stream.Read(buf)
	if readErr != nil {
		t.Fatalf("Error reading data from stream: %s", readErr)
	}
	if n != 5 {
		t.Fatalf("Unexpected number of bytes read:\nActual: %d\nExpected: 5", n)
	}
	if bytes.Compare(buf[:n], message) != 0 {
		t.Fatalf("Did not receive expected message:\nActual: %s\nExpectd: %s", buf, message)
	}

	headers := http.Header{
		"TestKey": []string{"TestVal"},
	}
	sendErr := stream.SendHeader(headers, false)
	if sendErr != nil {
		t.Fatalf("Error sending headers: %s", sendErr)
	}
	receiveHeaders, receiveErr := stream.ReceiveHeader()
	if receiveErr != nil {
		t.Fatalf("Error receiving headers: %s", receiveErr)
	}
	if len(receiveHeaders) != 1 {
		t.Fatalf("Unexpected number of headers:\nActual: %d\nExpecting:%d", len(receiveHeaders), 1)
	}
	testVal := receiveHeaders.Get("TestKey")
	if testVal != "TestVal" {
		t.Fatalf("Wrong test value:\nActual: %q\nExpecting: %q", testVal, "TestVal")
	}

	writeErr = stream.WriteData(message, true)
	if writeErr != nil {
		t.Fatalf("Error writing data")
	}

	smallBuf := make([]byte, 3)
	n, readErr = stream.Read(smallBuf)
	if readErr != nil {
		t.Fatalf("Error reading data from stream: %s", readErr)
	}
	if n != 3 {
		t.Fatalf("Unexpected number of bytes read:\nActual: %d\nExpected: 3", n)
	}
	if bytes.Compare(smallBuf[:n], []byte("hel")) != 0 {
		t.Fatalf("Did not receive expected message:\nActual: %s\nExpectd: %s", smallBuf[:n], message)
	}
	n, readErr = stream.Read(smallBuf)
	if readErr != nil {
		t.Fatalf("Error reading data from stream: %s", readErr)
	}
	if n != 2 {
		t.Fatalf("Unexpected number of bytes read:\nActual: %d\nExpected: 2", n)
	}
	if bytes.Compare(smallBuf[:n], []byte("lo")) != 0 {
		t.Fatalf("Did not receive expected message:\nActual: %s\nExpected: lo", smallBuf[:n])
	}

	n, readErr = stream.Read(buf)
	if readErr != io.EOF {
		t.Fatalf("Expected EOF reading from finished stream, read %d bytes", n)
	}

	streamCloseErr := stream.Close()
	if streamCloseErr != nil {
		t.Fatalf("Error closing stream: %s", streamCloseErr)
	}

	// Closing again should return nil
	streamCloseErr = stream.Close()
	if streamCloseErr != nil {
		t.Fatalf("Error closing stream: %s", streamCloseErr)
	}

	authenticated = false
	badStream, badStreamErr := spdyConn.CreateStream(http.Header{}, nil, false)
	if badStreamErr != nil {
		t.Fatalf("Error creating stream: %s", badStreamErr)
	}

	waitErr = badStream.Wait()
	if waitErr == nil {
		t.Fatalf("Did not receive error creating stream")
	}
	if waitErr != ErrReset {
		t.Fatalf("Unexpected error creating stream: %s", waitErr)
	}

	spdyCloseErr := spdyConn.Close()
	if spdyCloseErr != nil {
		t.Fatalf("Error closing spdy connection: %s", spdyCloseErr)
	}

	closeErr := server.Close()
	if closeErr != nil {
		t.Fatalf("Error shutting down server: %s", closeErr)
	}
	wg.Wait()
}

func TestPing(t *testing.T) {
	var wg sync.WaitGroup
	listen := "localhost:7443"
	server, serverErr := runServer(listen, &wg)
	if serverErr != nil {
		t.Fatalf("Error initializing server: %s", serverErr)
	}

	conn, dialErr := net.Dial("tcp", listen)
	if dialErr != nil {
		t.Fatalf("Error dialing server: %s", dialErr)
	}

	spdyConn, spdyErr := NewConnection(conn, false)
	if spdyErr != nil {
		t.Fatalf("Error creating spdy connection: %s", spdyErr)
	}
	go spdyConn.Serve(NoOpStreamHandler, RejectAuthHandler)

	pingTime, pingErr := spdyConn.Ping()
	if pingErr != nil {
		t.Fatalf("Error pinging server: %s", pingErr)
	}
	if pingTime == time.Duration(0) {
		t.Fatalf("Expecting non-zero ping time")
	}

	closeErr := server.Close()
	if closeErr != nil {
		t.Fatalf("Error shutting down server: %s", closeErr)
	}
	wg.Wait()
}

var authenticated bool

func authStreamHandler(header http.Header, slot uint8, parent uint32) bool {
	return authenticated
}

func runServer(listen string, wg *sync.WaitGroup) (io.Closer, error) {
	listener, listenErr := net.Listen("tcp", listen)
	if listenErr != nil {
		return nil, listenErr
	}
	wg.Add(1)
	go func() {
		for {
			conn, connErr := listener.Accept()
			if connErr != nil {
				break
			}

			spdyConn, _ := NewConnection(conn, true)
			go spdyConn.Serve(MirrorStreamHandler, authStreamHandler)

		}
		wg.Done()
	}()
	return listener, nil
}
