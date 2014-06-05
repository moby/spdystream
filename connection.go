package spdystream

import (
	"code.google.com/p/go.net/spdy"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

var (
	ErrInvalidStreamId   = errors.New("Invalid stream id")
	ErrTimeout           = errors.New("Timeout occured")
	ErrReset             = errors.New("Stream reset")
	ErrWriteClosedStream = errors.New("Write on closed stream")
)

type StreamHandler func(stream *Stream)

type AuthHandler func(header http.Header, slot uint8, parent uint32) bool

type Connection struct {
	conn   net.Conn
	framer *spdy.Framer

	streamLock sync.Mutex
	streams    map[spdy.StreamId]*Stream

	nextIdLock       sync.Mutex
	receiveIdLock    sync.Mutex
	nextStreamId     spdy.StreamId
	receivedStreamId spdy.StreamId
}

// NewConnection creates a new spdy connection from an existing
// network connection.
func NewConnection(conn net.Conn, server bool) (*Connection, error) {
	framer, framerErr := spdy.NewFramer(conn, conn)
	if framerErr != nil {
		return nil, framerErr
	}
	var sid spdy.StreamId
	var rid spdy.StreamId
	if server {
		sid = 2
		rid = 1
	} else {
		sid = 1
		rid = 2
	}

	session := &Connection{
		conn:   conn,
		framer: framer,

		streams:          make(map[spdy.StreamId]*Stream),
		nextStreamId:     sid,
		receivedStreamId: rid,
	}

	return session, nil
}

// Serve handles frames sent from the server, including reply frames
// which are needed to fully initiate connections.  Both clients and servers
// should call Serve in a separate goroutine before creating streams.
func (s *Connection) Serve(newHandler StreamHandler, authHandler AuthHandler) {
	for {
		readFrame, err := s.framer.ReadFrame()
		if err != nil {
			if err != io.EOF {
				fmt.Errorf("frame read error: %s", err)
			}
			break
		}

		var frameErr error
		switch frame := readFrame.(type) {
		case *spdy.SynStreamFrame:
			frameErr = s.handleStreamFrame(frame, newHandler, authHandler)
		case *spdy.SynReplyFrame:
			frameErr = s.handleReplyFrame(frame)
		case *spdy.DataFrame:
			frameErr = s.handleDataFrame(frame)
		case *spdy.RstStreamFrame:
			frameErr = s.handleResetFrame(frame)
		default:
			frameErr = fmt.Errorf("unhandled frame type: %T", frame)
		}

		if frameErr != nil {
			fmt.Errorf("frame handling error: %s", frameErr)
		}
	}
}

func (s *Connection) handleStreamFrame(frame *spdy.SynStreamFrame, newHandler StreamHandler, authHandler AuthHandler) error {
	validationErr := s.validateStreamId(frame.StreamId)
	if validationErr != nil {
		errorFrame := &spdy.RstStreamFrame{
			StreamId: frame.StreamId,
			Status:   spdy.ProtocolError,
		}
		writeErr := s.framer.WriteFrame(errorFrame)
		if writeErr != nil {
			return writeErr
		}
		return validationErr
	}

	stream := &Stream{
		streamId:  frame.StreamId,
		conn:      s,
		startChan: make(chan error),
		headers:   frame.Headers,
		finished:  (frame.CFHeader.Flags & spdy.ControlFlagFin) != 0x00,
		dataChan:  make(chan []byte),
	}

	s.streams[frame.StreamId] = stream

	if !authHandler(frame.Headers, frame.Slot, uint32(frame.AssociatedToStreamId)) {
		stream.Close()
		errorFrame := &spdy.RstStreamFrame{
			StreamId: frame.StreamId,
			Status:   spdy.RefusedStream,
		}
		writeErr := s.framer.WriteFrame(errorFrame)
		if writeErr != nil {
			return writeErr
		}
		return nil
	}

	replyFrame := &spdy.SynReplyFrame{
		StreamId: frame.StreamId,
		Headers:  http.Header{},
	}

	writeErr := s.framer.WriteFrame(replyFrame)
	if writeErr != nil {
		stream.Close()
		return writeErr
	}
	stream.replied = true

	newHandler(stream)

	return nil
}

func (s *Connection) handleReplyFrame(frame *spdy.SynReplyFrame) error {
	stream, streamOk := s.streams[frame.StreamId]
	if !streamOk {
		// Stream has already gone away
		return nil
	}
	if stream.replied {
		// Stream has already received reply
		return nil
	}
	stream.replied = true

	// TODO Check for error or fin
	close(stream.startChan)

	return nil
}

func (s *Connection) handleResetFrame(frame *spdy.RstStreamFrame) error {
	stream, streamOk := s.streams[frame.StreamId]
	if !streamOk {
		// Stream has already gone away
		return nil
	}
	stream.dataLock.Lock()
	if !stream.closed {
		close(stream.dataChan)
		stream.closed = true
	}
	stream.dataLock.Unlock()

	if !stream.replied {
		stream.replied = true
		stream.startChan <- ErrReset
		close(stream.startChan)
	}

	stream.finishLock.Lock()
	stream.finished = true
	stream.finishLock.Unlock()

	return nil
}

func (s *Connection) handleDataFrame(frame *spdy.DataFrame) error {
	stream, streamOk := s.streams[frame.StreamId]
	if !streamOk {
		// Stream has already gone away
		return nil
	}
	if !stream.replied {
		// No reply received...Protocol error?
		return nil
	}

	if len(frame.Data) > 0 {
		stream.dataLock.RLock()
		if !stream.closed {
			stream.dataChan <- frame.Data
		}
		stream.dataLock.RUnlock()
	}
	if (frame.Flags & spdy.DataFlagFin) != 0x00 {
		// synchronize closing channel
		stream.dataLock.Lock()
		if !stream.closed {
			close(stream.dataChan)
			stream.closed = true
		}
		stream.dataLock.Unlock()
	}
	return nil
}

// CreateStream creates a new spdy stream using the parameters for
// creating the stream frame.  The stream frame will be sent upon
// calling this function, however this function does not wait for
// the reply frame.  If waiting for the reply is desired, use
// the stream Wait or WaitTimeout function on the stream returned
// by this function.
func (s *Connection) CreateStream(headers http.Header, parent *Stream, fin bool) (*Stream, error) {
	streamId, streamErr := s.getNextStreamId()
	if streamErr != nil {
		return nil, streamErr
	}

	stream := &Stream{
		streamId:  streamId,
		parent:    parent,
		conn:      s,
		startChan: make(chan error),
		headers:   headers,
		dataChan:  make(chan []byte),
	}

	// Add stream to map, lock not necessary since streamId will be unique
	s.streams[streamId] = stream

	var flags spdy.ControlFlags
	if fin {
		flags = spdy.ControlFlagFin
		stream.finished = true
	}

	streamFrame := &spdy.SynStreamFrame{
		StreamId: spdy.StreamId(streamId),
		Headers:  headers,
		CFHeader: spdy.ControlFrameHeader{Flags: flags},
	}

	writeErr := s.framer.WriteFrame(streamFrame)
	if writeErr != nil {
		stream.Close()
		return nil, writeErr
	}

	return stream, nil
}

// Closes spdy connection, including network connection used
// to create the spdy connection.
func (s *Connection) Close() error {
	for _, stream := range s.streams {
		closeErr := stream.Close()
		if closeErr != nil {
			return closeErr
		}
	}
	return s.conn.Close()
}

// getNextStreamId returns the next sequential id
// every call should produce a unique value or an error
func (s *Connection) getNextStreamId() (spdy.StreamId, error) {
	s.nextIdLock.Lock()
	defer s.nextIdLock.Unlock()
	sid := s.nextStreamId
	if sid > 0x7fffffff {
		return 0, errors.New("Can't allocate new streams: uint32 overflow")
	}
	s.nextStreamId = s.nextStreamId + 2
	return sid, nil
}

func (s *Connection) validateStreamId(rid spdy.StreamId) error {
	s.receiveIdLock.Lock()
	defer s.receiveIdLock.Unlock()
	if rid > 0x7fffffff || rid < s.receivedStreamId {
		return ErrInvalidStreamId
	}
	s.receivedStreamId = rid + 2
	return nil
}

func (s *Connection) removeStream(stream *Stream) {
	delete(s.streams, stream.streamId)
}
