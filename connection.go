package spdystream

import (
	"code.google.com/p/go.net/spdy"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	ErrInvalidStreamId   = errors.New("Invalid stream id")
	ErrTimeout           = errors.New("Timeout occured")
	ErrReset             = errors.New("Stream reset")
	ErrWriteClosedStream = errors.New("Write on closed stream")
)

const (
	FRAME_WORKERS = 5
	QUEUE_SIZE    = 50
)

type StreamHandler func(stream *Stream)

type AuthHandler func(header http.Header, slot uint8, parent uint32) bool

type Connection struct {
	conn      net.Conn
	framer    *spdy.Framer
	writeLock sync.Mutex
	closeChan chan bool

	streamLock sync.RWMutex
	streams    map[spdy.StreamId]*Stream

	nextIdLock       sync.Mutex
	receiveIdLock    sync.Mutex
	nextStreamId     spdy.StreamId
	receivedStreamId spdy.StreamId

	pingIdLock sync.Mutex
	pingId     uint32
	pingChans  map[uint32]chan error
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
	var pid uint32
	if server {
		sid = 2
		rid = 1
		pid = 2
	} else {
		sid = 1
		rid = 2
		pid = 1
	}

	session := &Connection{
		conn:      conn,
		framer:    framer,
		closeChan: make(chan bool),

		streams:          make(map[spdy.StreamId]*Stream),
		nextStreamId:     sid,
		receivedStreamId: rid,

		pingId:    pid,
		pingChans: make(map[uint32]chan error),
	}

	return session, nil
}

// Ping sends a ping frame across the connection and
// returns the response time
func (s *Connection) Ping() (time.Duration, error) {
	pid := s.pingId
	s.pingIdLock.Lock()
	if s.pingId > 0x7ffffffe {
		s.pingId = s.pingId - 0x7ffffffe
	} else {
		s.pingId = s.pingId + 2
	}
	s.pingIdLock.Unlock()
	pingChan := make(chan error)
	s.pingChans[pid] = pingChan
	defer delete(s.pingChans, pid)

	frame := &spdy.PingFrame{Id: pid}
	startTime := time.Now()
	s.writeLock.Lock()
	writeErr := s.framer.WriteFrame(frame)
	s.writeLock.Unlock()
	if writeErr != nil {
		return time.Duration(0), writeErr
	}
	select {
	case <-s.closeChan:
		return time.Duration(0), errors.New("connection closed")
	case err, ok := <-pingChan:
		if ok && err != nil {
			return time.Duration(0), err
		}
		break
	}
	return time.Now().Sub(startTime), nil
}

// Serve handles frames sent from the server, including reply frames
// which are needed to fully initiate connections.  Both clients and servers
// should call Serve in a separate goroutine before creating streams.
func (s *Connection) Serve(newHandler StreamHandler) {
	// Parition queues to ensure stream frames are handled
	// by the same worker, ensuring order is maintained
	frameQueues := make([]*PriorityFrameQueue, FRAME_WORKERS)
	for i := 0; i < FRAME_WORKERS; i++ {
		frameQueues[i] = NewPriorityFrameQueue(QUEUE_SIZE)
		// Ensure frame queue is drained when connection is closed
		go func(frameQueue *PriorityFrameQueue) {
			<-s.closeChan
			frameQueue.Drain()
		}(frameQueues[i])

		go s.frameHandler(frameQueues[i], newHandler)
	}

	var partitionRoundRobin int
	for {
		readFrame, err := s.framer.ReadFrame()
		if err != nil {
			if err != io.EOF {
				fmt.Errorf("frame read error: %s", err)
			}
			break
		}
		var priority uint8
		var partition int
		switch frame := readFrame.(type) {
		case *spdy.SynStreamFrame:
			priority = frame.Priority
			partition = int(frame.StreamId % FRAME_WORKERS)
		case *spdy.SynReplyFrame:
			priority = s.getStreamPriority(frame.StreamId)
			partition = int(frame.StreamId % FRAME_WORKERS)
		case *spdy.DataFrame:
			priority = s.getStreamPriority(frame.StreamId)
			partition = int(frame.StreamId % FRAME_WORKERS)
		case *spdy.RstStreamFrame:
			priority = s.getStreamPriority(frame.StreamId)
			partition = int(frame.StreamId % FRAME_WORKERS)
		case *spdy.HeadersFrame:
			priority = s.getStreamPriority(frame.StreamId)
			partition = int(frame.StreamId % FRAME_WORKERS)
		case *spdy.PingFrame:
			priority = 0
			partition = partitionRoundRobin
			partitionRoundRobin = (partitionRoundRobin + 1) % FRAME_WORKERS
		default:
			priority = 7
			partition = partitionRoundRobin
			partitionRoundRobin = (partitionRoundRobin + 1) % FRAME_WORKERS
		}
		frameQueues[partition].Push(readFrame, priority)
	}
}

func (s *Connection) frameHandler(frameQueue *PriorityFrameQueue, newHandler StreamHandler) {
	for {
		popFrame := frameQueue.Pop()
		if popFrame == nil {
			return
		}

		var frameErr error
		switch frame := popFrame.(type) {
		case *spdy.SynStreamFrame:
			frameErr = s.handleStreamFrame(frame, newHandler)
		case *spdy.SynReplyFrame:
			frameErr = s.handleReplyFrame(frame)
		case *spdy.DataFrame:
			frameErr = s.handleDataFrame(frame)
		case *spdy.RstStreamFrame:
			frameErr = s.handleResetFrame(frame)
		case *spdy.HeadersFrame:
			frameErr = s.handleHeaderFrame(frame)
		case *spdy.PingFrame:
			frameErr = s.handlePingFrame(frame)
		default:
			frameErr = fmt.Errorf("unhandled frame type: %T", frame)
		}

		if frameErr != nil {
			fmt.Errorf("frame handling error: %s", frameErr)
		}
	}
}

func (s *Connection) getStreamPriority(streamId spdy.StreamId) uint8 {
	s.streamLock.RLock()
	stream, streamOk := s.streams[streamId]
	s.streamLock.RUnlock()
	if !streamOk {
		return 7
	}
	return stream.priority
}

func (s *Connection) handleStreamFrame(frame *spdy.SynStreamFrame, newHandler StreamHandler) error {
	validationErr := s.validateStreamId(frame.StreamId)
	if validationErr != nil {
		errorFrame := &spdy.RstStreamFrame{
			StreamId: frame.StreamId,
			Status:   spdy.ProtocolError,
		}
		s.writeLock.Lock()
		writeErr := s.framer.WriteFrame(errorFrame)
		s.writeLock.Unlock()
		if writeErr != nil {
			return writeErr
		}
		return validationErr
	}

	var parent *Stream
	if frame.AssociatedToStreamId != spdy.StreamId(0) {
		s.streamLock.RLock()
		parent = s.streams[frame.AssociatedToStreamId]
		s.streamLock.RUnlock()
	}

	stream := &Stream{
		streamId:   frame.StreamId,
		parent:     parent,
		conn:       s,
		startChan:  make(chan error),
		headers:    frame.Headers,
		finished:   (frame.CFHeader.Flags & spdy.ControlFlagFin) != 0x00,
		dataChan:   make(chan []byte),
		headerChan: make(chan http.Header),
		closeChan:  make(chan bool),
	}

	s.streamLock.Lock()
	s.streams[frame.StreamId] = stream
	s.streamLock.Unlock()

	newHandler(stream)

	return nil
}

func (s *Connection) handleReplyFrame(frame *spdy.SynReplyFrame) error {
	s.streamLock.RLock()
	stream, streamOk := s.streams[frame.StreamId]
	s.streamLock.RUnlock()
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
	s.streamLock.RLock()
	stream, streamOk := s.streams[frame.StreamId]
	s.streamLock.RUnlock()
	if !streamOk {
		// Stream has already gone away
		return nil
	}
	stream.dataLock.Lock()
	select {
	case <-stream.closeChan:
		break
	default:
		close(stream.dataChan)
		close(stream.closeChan)
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

func (s *Connection) handleHeaderFrame(frame *spdy.HeadersFrame) error {
	s.streamLock.RLock()
	stream, streamOk := s.streams[frame.StreamId]
	s.streamLock.RUnlock()
	if !streamOk {
		// Stream has already gone away
		return nil
	}
	if !stream.replied {
		// No reply received...Protocol error?
		return nil
	}

	// TODO limit headers while not blocking (use buffered chan or goroutine?)
	select {
	case <-stream.closeChan:
		return nil
	case stream.headerChan <- frame.Headers:
	}

	// TODO handle fin header

	return nil
}

func (s *Connection) handleDataFrame(frame *spdy.DataFrame) error {
	s.streamLock.RLock()
	stream, streamOk := s.streams[frame.StreamId]
	s.streamLock.RUnlock()
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
		select {
		case <-stream.closeChan:
			break
		default:
			stream.dataChan <- frame.Data
		}
		stream.dataLock.RUnlock()
	}
	if (frame.Flags & spdy.DataFlagFin) != 0x00 {
		// synchronize closing channel
		stream.dataLock.Lock()
		select {
		case <-stream.closeChan:
			break
		default:
			close(stream.dataChan)
			close(stream.closeChan)
		}
		stream.dataLock.Unlock()
	}
	return nil
}

func (s *Connection) handlePingFrame(frame *spdy.PingFrame) error {
	if s.pingId&0x01 != frame.Id&0x01 {
		s.writeLock.Lock()
		defer s.writeLock.Unlock()
		return s.framer.WriteFrame(frame)
	}
	pingChan, pingOk := s.pingChans[frame.Id]
	if pingOk {
		close(pingChan)
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
	streamId := s.getNextStreamId()
	if streamId == 0 {
		return nil, fmt.Errorf("Unable to get new stream id")
	}

	stream := &Stream{
		streamId:   streamId,
		parent:     parent,
		conn:       s,
		startChan:  make(chan error),
		headers:    headers,
		dataChan:   make(chan []byte),
		headerChan: make(chan http.Header),
		closeChan:  make(chan bool),
	}

	s.streamLock.Lock()
	s.streams[streamId] = stream
	s.streamLock.Unlock()

	return stream, s.sendStream(stream, fin)
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
	close(s.closeChan)
	return s.conn.Close()
}

func (s *Connection) sendHeaders(headers http.Header, stream *Stream, fin bool) error {
	var flags spdy.ControlFlags
	if fin {
		flags = spdy.ControlFlagFin
	}

	headerFrame := &spdy.HeadersFrame{
		StreamId: stream.streamId,
		Headers:  headers,
		CFHeader: spdy.ControlFrameHeader{Flags: flags},
	}

	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	return s.framer.WriteFrame(headerFrame)
}

func (s *Connection) sendReply(headers http.Header, stream *Stream, fin bool) error {
	var flags spdy.ControlFlags
	if fin {
		flags = spdy.ControlFlagFin
	}

	replyFrame := &spdy.SynReplyFrame{
		StreamId: stream.streamId,
		Headers:  headers,
		CFHeader: spdy.ControlFrameHeader{Flags: flags},
	}

	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	return s.framer.WriteFrame(replyFrame)
}

func (s *Connection) sendReset(status spdy.RstStreamStatus, stream *Stream) error {
	resetFrame := &spdy.RstStreamFrame{
		StreamId: stream.streamId,
		Status:   status,
	}

	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	return s.framer.WriteFrame(resetFrame)
}

func (s *Connection) sendStream(stream *Stream, fin bool) error {
	var flags spdy.ControlFlags
	if fin {
		flags = spdy.ControlFlagFin
		stream.finished = true
	}

	var parentId spdy.StreamId
	if stream.parent != nil {
		parentId = stream.parent.streamId
	}

	streamFrame := &spdy.SynStreamFrame{
		StreamId:             spdy.StreamId(stream.streamId),
		AssociatedToStreamId: spdy.StreamId(parentId),
		Headers:              stream.headers,
		CFHeader:             spdy.ControlFrameHeader{Flags: flags},
	}

	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	return s.framer.WriteFrame(streamFrame)
}

// getNextStreamId returns the next sequential id
// every call should produce a unique value or an error
func (s *Connection) getNextStreamId() spdy.StreamId {
	s.nextIdLock.Lock()
	defer s.nextIdLock.Unlock()
	sid := s.nextStreamId
	if sid > 0x7fffffff {
		return 0
	}
	s.nextStreamId = s.nextStreamId + 2
	return sid
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
	s.streamLock.Lock()
	delete(s.streams, stream.streamId)
	s.streamLock.Unlock()
}
