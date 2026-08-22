package ioutils

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
)

type ResponseModifier interface {
	http.ResponseWriter
	http.Flusher

	RawBody() []byte

	RawHeaders() ([]byte, error)

	StatusCode() int

	OverrideBody(b []byte)

	OverrideHeader(b []byte) error

	OverrideStatusCode(statusCode int)

	FlushAll() error

	Hijacked() bool
}

func NewResponseModifier(rw http.ResponseWriter) ResponseModifier {
	return &responseModifier{rw: rw, header: make(http.Header)}
}

const maxBufferSize = 64 * 1024

type responseModifier struct {
	rw         http.ResponseWriter
	body       []byte
	header     http.Header
	statusCode int
	hijacked   bool
}

func (rm *responseModifier) Hijacked() bool {
	return rm.hijacked
}

func (rm *responseModifier) WriteHeader(s int) {
	if rm.hijacked {
		rm.rw.WriteHeader(s)
		return
	}

	rm.statusCode = s
}

func (rm *responseModifier) Header() http.Header {
	if rm.hijacked {
		return rm.rw.Header()
	}

	return rm.header
}

func (rm *responseModifier) StatusCode() int {
	return rm.statusCode
}

func (rm *responseModifier) OverrideBody(b []byte) {
	rm.body = b
}

func (rm *responseModifier) OverrideStatusCode(statusCode int) {
	rm.statusCode = statusCode
}

func (rm *responseModifier) OverrideHeader(b []byte) error {
	header := http.Header{}
	if err := json.Unmarshal(b, &header); err != nil {
		return err
	}
	rm.header = header
	return nil
}

func (rm *responseModifier) Write(b []byte) (int, error) {
	if rm.hijacked {
		return rm.rw.Write(b)
	}

	if len(rm.body)+len(b) > maxBufferSize {
		rm.Flush()
	}
	rm.body = append(rm.body, b...)
	return len(b), nil
}

func (rm *responseModifier) RawBody() []byte {
	return rm.body
}

func (rm *responseModifier) RawHeaders() ([]byte, error) {
	var b bytes.Buffer
	if err := rm.header.Write(&b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (rm *responseModifier) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	rm.hijacked = true
	rm.FlushAll()

	hijacker, ok := rm.rw.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("Internal response writer doesn't support the Hijacker interface")
	}
	return hijacker.Hijack()
}

func (rm *responseModifier) Flush() {
	flusher, ok := rm.rw.(http.Flusher)
	if !ok {
		return
	}

	rm.FlushAll()
	flusher.Flush()
}

func (rm *responseModifier) FlushAll() error {
	for k, vv := range rm.header {
		for _, v := range vv {
			rm.rw.Header().Add(k, v)
		}
	}

	if rm.statusCode > 0 {
		rm.rw.WriteHeader(rm.statusCode)
	}

	var err error
	if len(rm.body) > 0 {
		var n int
		n, err = rm.rw.Write(rm.body)
		rm.body = rm.body[n:]
	}

	rm.statusCode = 0
	rm.header = http.Header{}
	return err
}
