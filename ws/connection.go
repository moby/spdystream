/*
   Copyright 2014-2021 Docker Inc.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package ws

import (
	"fmt"
	"io"
	"time"

	"github.com/gorilla/websocket"
)

// Wrap an HTTP2 connection over WebSockets and
// use the underlying WebSocket framing for proxy
// compatibility.
type Conn struct {
	*websocket.Conn
	reader io.Reader
}

func NewConnection(w *websocket.Conn) *Conn {
	return &Conn{Conn: w}
}

func (c *Conn) Write(b []byte) (int, error) {
	err := c.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *Conn) Read(b []byte) (int, error) {
	if c.reader == nil {
		if err := c.nextReader(); err != nil {
			return 0, err
		}
	}

	for {
		n, err := c.reader.Read(b)
		if err != nil {
			if err != io.EOF {
				return n, err
			}

			// get next reader if there is no data in the current one
			if err := c.nextReader(); err != nil {
				return 0, err
			}
			continue
		}
		return n, nil
	}
}

func (c *Conn) nextReader() error {
	t, r, err := c.NextReader()
	if err != nil {
		return err
	}

	if t != websocket.BinaryMessage {
		return fmt.Errorf("ws: non-binary message in stream")
	}
	c.reader = r
	return nil
}

func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.Conn.SetReadDeadline(t); err != nil {
		return err
	}
	if err := c.Conn.SetWriteDeadline(t); err != nil {
		return err
	}
	return nil
}

func (c *Conn) Close() error {
	err := c.Conn.Close()
	return err
}
