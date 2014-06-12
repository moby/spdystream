package spdystream

import (
	"io"
	"net/http"
)

// MirrorStreamHandler mirrors all streams.
func MirrorStreamHandler(stream *Stream) http.Header {
	go func() {
		io.Copy(stream, stream)
		stream.Close()
	}()
	go func() {
		for {
			header, receiveErr := stream.ReceiveHeader()
			if receiveErr != nil {
				return
			}
			sendErr := stream.SendHeader(header, false)
			if sendErr != nil {
				return
			}
		}
	}()
	return nil
}

// NoopStreamHandler does nothing when stream connects, most
// likely used with RejectAuthHandler which will not allow any
// streams to make it to the stream handler.
func NoOpStreamHandler(stream *Stream) http.Header { return nil }

// NoAuthHandler skips authentication.
func NoAuthHandler(header http.Header, slot uint8, parent uint32) bool {
	return true
}

// RejectAuthHandler rejects all remotely initiated connections.
func RejectAuthHandler(header http.Header, slot uint8, parent uint32) bool {
	return false
}
