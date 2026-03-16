// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package spdy

import (
	"encoding/binary"
	"io"
	"net/http"
	"strings"
)

func (frame *SynStreamFrame) write(f *Framer) error {
	return f.writeSynStreamFrame(frame)
}

func (frame *SynReplyFrame) write(f *Framer) error {
	return f.writeSynReplyFrame(frame)
}

func (frame *RstStreamFrame) write(f *Framer) error {
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	frame.CFHeader.version = Version
	frame.CFHeader.frameType = TypeRstStream
	frame.CFHeader.Flags = 0
	frame.CFHeader.length = 8

	// Serialize frame to Writer.
	if err := writeControlFrameHeader(f.w, frame.CFHeader); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.StreamId); err != nil {
		return err
	}
	if frame.Status == 0 {
		return &Error{InvalidControlFrame, frame.StreamId}
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.Status); err != nil {
		return err
	}
	return nil
}

func (frame *SettingsFrame) write(f *Framer) error {
	frame.CFHeader.version = Version
	frame.CFHeader.frameType = TypeSettings
	frame.CFHeader.length = uint32(len(frame.FlagIdValues)*8 + 4)

	// Serialize frame to Writer.
	if err := writeControlFrameHeader(f.w, frame.CFHeader); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, uint32(len(frame.FlagIdValues))); err != nil {
		return err
	}
	for _, flagIdValue := range frame.FlagIdValues {
		flagId := uint32(flagIdValue.Flag)<<24 | uint32(flagIdValue.Id)
		if err := binary.Write(f.w, binary.BigEndian, flagId); err != nil {
			return err
		}
		if err := binary.Write(f.w, binary.BigEndian, flagIdValue.Value); err != nil {
			return err
		}
	}
	return nil
}

func (frame *PingFrame) write(f *Framer) error {
	if frame.Id == 0 {
		return &Error{ZeroStreamId, 0}
	}
	frame.CFHeader.version = Version
	frame.CFHeader.frameType = TypePing
	frame.CFHeader.Flags = 0
	frame.CFHeader.length = 4

	// Serialize frame to Writer.
	if err := writeControlFrameHeader(f.w, frame.CFHeader); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.Id); err != nil {
		return err
	}
	return nil
}

func (frame *GoAwayFrame) write(f *Framer) error {
	frame.CFHeader.version = Version
	frame.CFHeader.frameType = TypeGoAway
	frame.CFHeader.Flags = 0
	frame.CFHeader.length = 8

	// Serialize frame to Writer.
	if err := writeControlFrameHeader(f.w, frame.CFHeader); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.LastGoodStreamId); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.Status); err != nil {
		return err
	}
	return nil
}

func (frame *HeadersFrame) write(f *Framer) error {
	return f.writeHeadersFrame(frame)
}

func (frame *WindowUpdateFrame) write(f *Framer) error {
	frame.CFHeader.version = Version
	frame.CFHeader.frameType = TypeWindowUpdate
	frame.CFHeader.Flags = 0
	frame.CFHeader.length = 8

	// Serialize frame to Writer.
	if err := writeControlFrameHeader(f.w, frame.CFHeader); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.StreamId); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.DeltaWindowSize); err != nil {
		return err
	}
	return nil
}

func (frame *DataFrame) write(f *Framer) error {
	return f.writeDataFrame(frame)
}

// WriteFrame writes a frame.
func (f *Framer) WriteFrame(frame Frame) error {
	return frame.write(f)
}

func writeControlFrameHeader(w io.Writer, h ControlFrameHeader) error {
	if err := binary.Write(w, binary.BigEndian, 0x8000|h.version); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, h.frameType); err != nil {
		return err
	}
	flagsAndLength := uint32(h.Flags)<<24 | h.length
	if err := binary.Write(w, binary.BigEndian, flagsAndLength); err != nil {
		return err
	}
	return nil
}

//nolint:unparam // result n is never used
func writeHeaderValueBlock(w io.Writer, h http.Header) (int, error) {
	n := 0
	if err := binary.Write(w, binary.BigEndian, uint32(len(h))); err != nil {
		return n, err
	}
	n += 2
	for name, values := range h {
		if err := binary.Write(w, binary.BigEndian, uint32(len(name))); err != nil {
			return n, err
		}
		n += 2
		name = strings.ToLower(name)
		if _, err := io.WriteString(w, name); err != nil {
			return n, err
		}
		n += len(name)
		v := strings.Join(values, headerValueSeparator)
		if err := binary.Write(w, binary.BigEndian, uint32(len(v))); err != nil {
			return n, err
		}
		n += 2
		if _, err := io.WriteString(w, v); err != nil {
			return n, err
		}
		n += len(v)
	}
	return n, nil
}

func (f *Framer) writeSynStreamFrame(frame *SynStreamFrame) error {
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	// Marshal the headers.
	var writer io.Writer = f.headerBuf
	if !f.headerCompressionDisabled {
		writer = f.headerCompressor
	}
	if _, err := writeHeaderValueBlock(writer, frame.Headers); err != nil {
		return err
	}
	if !f.headerCompressionDisabled {
		_ = f.headerCompressor.Flush()
	}

	// Set ControlFrameHeader.
	frame.CFHeader.version = Version
	frame.CFHeader.frameType = TypeSynStream
	frame.CFHeader.length = uint32(len(f.headerBuf.Bytes()) + 10)

	// Serialize frame to Writer.
	if err := writeControlFrameHeader(f.w, frame.CFHeader); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.StreamId); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.AssociatedToStreamId); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.Priority<<5); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.Slot); err != nil {
		return err
	}
	if _, err := f.w.Write(f.headerBuf.Bytes()); err != nil {
		return err
	}
	f.headerBuf.Reset()
	return nil
}

func (f *Framer) writeSynReplyFrame(frame *SynReplyFrame) error {
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	// Marshal the headers.
	var writer io.Writer = f.headerBuf
	if !f.headerCompressionDisabled {
		writer = f.headerCompressor
	}
	if _, err := writeHeaderValueBlock(writer, frame.Headers); err != nil {
		return err
	}
	if !f.headerCompressionDisabled {
		_ = f.headerCompressor.Flush()
	}

	// Set ControlFrameHeader.
	frame.CFHeader.version = Version
	frame.CFHeader.frameType = TypeSynReply
	frame.CFHeader.length = uint32(len(f.headerBuf.Bytes()) + 4)

	// Serialize frame to Writer.
	if err := writeControlFrameHeader(f.w, frame.CFHeader); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.StreamId); err != nil {
		return err
	}
	if _, err := f.w.Write(f.headerBuf.Bytes()); err != nil {
		return err
	}
	f.headerBuf.Reset()
	return nil
}

func (f *Framer) writeHeadersFrame(frame *HeadersFrame) error {
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	// Marshal the headers.
	var writer io.Writer = f.headerBuf
	if !f.headerCompressionDisabled {
		writer = f.headerCompressor
	}
	if _, err := writeHeaderValueBlock(writer, frame.Headers); err != nil {
		return err
	}
	if !f.headerCompressionDisabled {
		_ = f.headerCompressor.Flush()
	}

	// Set ControlFrameHeader.
	frame.CFHeader.version = Version
	frame.CFHeader.frameType = TypeHeaders
	frame.CFHeader.length = uint32(len(f.headerBuf.Bytes()) + 4)

	// Serialize frame to Writer.
	if err := writeControlFrameHeader(f.w, frame.CFHeader); err != nil {
		return err
	}
	if err := binary.Write(f.w, binary.BigEndian, frame.StreamId); err != nil {
		return err
	}
	if _, err := f.w.Write(f.headerBuf.Bytes()); err != nil {
		return err
	}
	f.headerBuf.Reset()
	return nil
}

func (f *Framer) writeDataFrame(frame *DataFrame) error {
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	if frame.StreamId&0x80000000 != 0 || len(frame.Data) > MaxDataLength {
		return &Error{InvalidDataFrame, frame.StreamId}
	}

	// Serialize frame to Writer.
	if err := binary.Write(f.w, binary.BigEndian, frame.StreamId); err != nil {
		return err
	}
	flagsAndLength := uint32(frame.Flags)<<24 | uint32(len(frame.Data))
	if err := binary.Write(f.w, binary.BigEndian, flagsAndLength); err != nil {
		return err
	}
	if _, err := f.w.Write(frame.Data); err != nil {
		return err
	}
	return nil
}
