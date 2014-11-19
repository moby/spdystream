package spdystream

import (
	"encoding/gob"
	"log"
	"net"
	"os"
	"time"

	"code.google.com/p/go.net/spdy"
)

var (
	DEBUG     = os.Getenv("DEBUG")
	debugChan chan *DebugFrame
)

type DebugFrame struct {
	Local  string
	Remote string
	T      time.Time
	Frame  spdy.Frame
	Send   bool
}

func registerGob() {
	gob.Register(&DebugFrame{})
	gob.Register(&spdy.SynStreamFrame{})
	gob.Register(&spdy.SynReplyFrame{})
	gob.Register(&spdy.DataFrame{})
	gob.Register(&spdy.HeadersFrame{})
	gob.Register(&spdy.GoAwayFrame{})
	gob.Register(&spdy.RstStreamFrame{})
	gob.Register(&spdy.PingFrame{})
	gob.Register(&spdy.SettingsFrame{})
	gob.Register(&spdy.WindowUpdateFrame{})
}

func InitializeDebugServer() {
	registerGob()
}

func init() {
	if debugServer := os.Getenv("SPDYSTREAM_DEBUG"); debugServer != "" {
		registerGob()
		c := make(chan *DebugFrame)
		w, err := net.Dial("tcp", debugServer)
		if err != nil {
			log.Printf("Cannot connect to debug server at %s", debugServer)
		} else {
			debugChan = c
			go func() {
				encoder := gob.NewEncoder(w)
				for {
					if err := encoder.Encode(<-c); err != nil {
						log.Printf("Error sending debug frame: %s", err)
						break
					}
				}
			}()
		}
	}

}

func debugMessage(fmt string, args ...interface{}) {
	if DEBUG != "" {
		log.Printf(fmt, args...)
	}
}

func debugFrame(c *Connection, f spdy.Frame, send bool) {
	if debugChan != nil {
		debugChan <- &DebugFrame{
			Local:  c.conn.LocalAddr().String(),
			Remote: c.conn.RemoteAddr().String(),
			T:      time.Now().UTC(),
			Frame:  f,
			Send:   send,
		}
	}
}
