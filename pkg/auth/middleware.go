package auth

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sysson/dink/pkg/identity"
	authv1 "github.com/sysson/dink/sdk/auth/v1"
)

const (
	maxBodySize   = 4 * 1024 * 1024
	maxBufferSize = 64 * 1024
)

func Middleware(chain *Chain) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if chain.Len() == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authMethod := "TLS"
			id, ok := identity.FromContext(r.Context())
			if !ok {
				authMethod = ""
			}

			body, err := requestBody(r)
			if err != nil {
				http.Error(w, "authorization request body error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			headers := flattenHeader(r.Header)

			reqResp, err := chain.AuthZReq(r.Context(), &authv1.AuthZReqRequest{
				Namespace:       id.Namespace,
				User:            id.CommonName,
				UserAuthnMethod: authMethod,
				RequestMethod:   r.Method,
				RequestUri:      r.RequestURI,
				RequestBody:     body,
				Headers:         headers,
			})
			if err != nil {
				http.Error(w, "authorization plugin error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if !reqResp.GetAllow() {
				http.Error(w, "request denied by authorization plugin: "+reqResp.GetMsg(), http.StatusForbidden)
				return
			}

			rec := newResponseModifier(w)
			next.ServeHTTP(rec, r)

			resResp, err := chain.AuthZRes(r.Context(), &authv1.AuthZResRequest{
				Namespace:          id.Namespace,
				User:               id.CommonName,
				UserAuthnMethod:    authMethod,
				RequestMethod:      r.Method,
				RequestUri:         r.RequestURI,
				RequestBody:        body,
				RequestHeader:      headers,
				ResponseBody:       responseBody(r.RequestURI, rec.header, rec.rawBody()),
				ResponseHeader:     flattenHeader(rec.header),
				ResponseStatusCode: int32(rec.status),
			})
			if err != nil {
				http.Error(w, "authorization plugin error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if !resResp.GetAllow() {
				http.Error(w, "response denied by authorization plugin: "+resResp.GetMsg(), http.StatusForbidden)
				return
			}

			if !rec.hijacked {
				rec.commit()
			}
		})
	}
}

func requestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || !sendBody(r.RequestURI, r.Header) {
		return nil, nil
	}
	reader := bufio.NewReaderSize(r.Body, maxBodySize+1)
	r.Body = &readCloser{Reader: reader, closer: r.Body}
	body, err := reader.Peek(maxBodySize + 1)
	if err == nil || err == bufio.ErrBufferFull {
		return nil, fmt.Errorf("request body too large for authorization plugin: size exceeds %d bytes", maxBodySize)
	}
	if err != io.EOF {
		return nil, err
	}
	return append([]byte(nil), body...), nil
}

func sendBody(requestURI string, header http.Header) bool {
	parsed, err := url.Parse(requestURI)
	if err != nil {
		return false
	}
	isAuth, err := regexp.MatchString(`^[^/]*(/v\d[\d.]*/)?auth.*`, parsed.Path)
	if err != nil || isAuth {
		return false
	}
	contentType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && contentType == "application/json"
}

func responseBody(requestURI string, header http.Header, body []byte) []byte {
	if !sendBody(requestURI, header) {
		return nil
	}
	return body
}

type readCloser struct {
	io.Reader
	closer io.Closer
}

func (r *readCloser) Close() error {
	return r.closer.Close()
}

type responseModifier struct {
	rw          http.ResponseWriter
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
	committed   bool
	hijacked    bool
}

func newResponseModifier(rw http.ResponseWriter) *responseModifier {
	return &responseModifier{rw: rw, header: make(http.Header), status: http.StatusOK}
}

func (r *responseModifier) Header() http.Header {
	return r.header
}

func (r *responseModifier) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if r.hijacked || r.committed {
		return r.rw.Write(body)
	}
	if r.body.Len()+len(body) > maxBufferSize {
		if err := r.flush(); err != nil {
			return 0, err
		}
		return r.rw.Write(body)
	}
	return r.body.Write(body)
}

func (r *responseModifier) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
}

func (r *responseModifier) copyHeaders() {
	for key, values := range r.header {
		r.rw.Header()[key] = append([]string(nil), values...)
	}
}

func (r *responseModifier) commit() {
	if r.committed {
		return
	}
	r.copyHeaders()
	r.rw.WriteHeader(r.status)
	_, _ = r.rw.Write(r.body.Bytes())
	r.body.Reset()
	r.committed = true
}

func (r *responseModifier) flush() error {
	if r.hijacked {
		if flusher, ok := r.rw.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	}
	if !r.committed {
		r.copyHeaders()
		r.rw.WriteHeader(r.status)
		r.committed = true
	}
	if r.body.Len() > 0 {
		if _, err := r.rw.Write(r.body.Bytes()); err != nil {
			return err
		}
		r.body.Reset()
	}
	if flusher, ok := r.rw.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (r *responseModifier) Flush() {
	_ = r.flush()
}

func (r *responseModifier) rawBody() []byte {
	if r.hijacked || r.committed {
		return nil
	}
	return r.body.Bytes()
}

func (r *responseModifier) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.rw.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	r.hijacked = true
	r.commit()
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	return conn, rw, nil
}

func (r *responseModifier) ReadFrom(reader io.Reader) (int64, error) {
	if r.hijacked || r.committed {
		return io.Copy(r.rw, reader)
	}
	var total int64
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := reader.Read(buffer)
		if read > 0 {
			written, writeErr := r.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

func flattenHeader(h http.Header) map[string]string {
	flat := make(map[string]string, len(h))
	for k, v := range h {
		if strings.EqualFold(k, "Authorization") ||
			strings.EqualFold(k, "X-Registry-Config") ||
			strings.EqualFold(k, "X-Registry-Auth") {
			continue
		}
		for _, value := range v {
			flat[k] = value
		}
	}
	return flat
}
