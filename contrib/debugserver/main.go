package main

import (
	"encoding/gob"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"code.google.com/p/go.net/spdy"
	"github.com/docker/spdystream"
)

func init() {
	spdystream.InitializeDebugServer()
}

func main() {
	l, err := net.Listen("tcp", "localhost:9399")
	if err != nil {
		log.Fatalf("Error opening listener: %s", err)
	}

	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatalf("Accept error: %s")
		}
		go SpawnDebugHandler(c)
	}

}

func SpawnDebugHandler(c net.Conn) {
	defer c.Close()
	decoder := gob.NewDecoder(c)
	for {
		var d spdystream.DebugFrame
		if err := decoder.Decode(&d); err == io.EOF {
			return
		} else if err != nil {
			panic(err)
		}
		if d.Frame == nil {
			continue
		}
		handleFrame(&d)
	}
}

func formatHeaders(h http.Header) string {
	var headers []string
	for k, v := range h {
		if len(v) == 1 {
			headers = append(headers, fmt.Sprintf("%s=%q", k, v[0]))
		} else {
			headers = append(headers, fmt.Sprintf("%s=%#v", k, v))
		}
	}
	return strings.Join(headers, " ")
}

func formatData(d []byte) string {
	if len(d) < 20 {
		return fmt.Sprintf("%x", d)
	} else {
		return fmt.Sprintf("%x..", d[:19])
	}
}

func isFin(h spdy.ControlFrameHeader) bool {
	return h.Flags&spdy.ControlFlagFin == 1
}

func handleFrame(f *spdystream.DebugFrame) {
	switch v := f.Frame.(type) {
	case *spdy.SynStreamFrame:
		formatLine(f, "stream", Green, fmt.Sprintf("%s", formatHeaders(v.Headers)), isFin(v.CFHeader), v.StreamId, f.T)
	case *spdy.SynReplyFrame:
		formatLine(f, "reply", Green, fmt.Sprintf("%s", formatHeaders(v.Headers)), isFin(v.CFHeader), v.StreamId, f.T)
	case *spdy.DataFrame:
		formatLine(f, "data", Cyan, fmt.Sprintf("(len: %d) %s", len(v.Data), formatData(v.Data)), v.Flags&spdy.DataFlagFin == 1, v.StreamId, f.T)
	case *spdy.PingFrame:
		formatLine(f, "ping", Magenta, fmt.Sprintf("(id: %d)", v.Id), isFin(v.CFHeader), 0, f.T)
	case *spdy.GoAwayFrame:
		formatLine(f, "goaway", Red, fmt.Sprintf("(last: %d, status: %d)", v.LastGoodStreamId, v.Status), isFin(v.CFHeader), 0, f.T)
	case *spdy.RstStreamFrame:
		formatLine(f, "reset", Red, fmt.Sprintf("(status: %d)", v.Status), isFin(v.CFHeader), v.StreamId, f.T)
	case *spdy.HeadersFrame:
		formatLine(f, "header", Cyan, formatHeaders(v.Headers), isFin(v.CFHeader), v.StreamId, f.T)
	case *spdy.SettingsFrame:
		formatLine(f, "settng", Yellow, "", isFin(v.CFHeader), 0, f.T)
	case *spdy.WindowUpdateFrame:
		formatLine(f, "window", Yellow, fmt.Sprintf("delta: %d bytes", v.DeltaWindowSize), isFin(v.CFHeader), v.StreamId, f.T)
	default:
		panic("Unsupported type")
	}
}

func filterLocalhost(name string) string {
	if strings.HasPrefix(name, "127.0.0.1:") {
		return name[9:]
	}
	if strings.HasPrefix(name, "localhost:") {
		return name[9:]
	}
	return name
}

func getName(f *spdystream.DebugFrame) string {
	d := "<-"
	if f.Send {
		d = "->"
	}
	return fmt.Sprintf("%s%s%s", filterLocalhost(f.Local), d, filterLocalhost(f.Remote))
}

func formatLine(f *spdystream.DebugFrame, frameType, color, content string, fin bool, id spdy.StreamId, t time.Time) {
	eof := ""
	if fin {
		eof = "fin"
	}
	fmt.Printf("%s%s %5d  %-6s %3s %s%s\n", color, getName(f), id, frameType, eof, content, Clear)

}

const Black = "\x1b[30;1m"
const Red = "\x1b[31;1m"
const Green = "\x1b[32;1m"
const Yellow = "\x1b[33;1m"
const Blue = "\x1b[34;1m"
const Magenta = "\x1b[35;1m"
const Cyan = "\x1b[36;1m"
const White = "\x1b[37;1m"
const Clear = "\x1b[0m"
