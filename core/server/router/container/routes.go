package container

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/moby/moby/api/types"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

func (cr *containerRouter) headContainersArchive(w http.ResponseWriter, r *http.Request) error {
	return unsupportedContainerEndpoint(r)
}

func (cr *containerRouter) getContainersExport(w http.ResponseWriter, r *http.Request) error {
	return unsupportedContainerEndpoint(r)
}

func (cr *containerRouter) getContainersChanges(w http.ResponseWriter, r *http.Request) error {
	return unsupportedContainerEndpoint(r)
}

func (cr *containerRouter) getContainersTop(w http.ResponseWriter, r *http.Request) error {
	return unsupportedContainerEndpoint(r)
}

func (cr *containerRouter) wsContainersAttach(w http.ResponseWriter, r *http.Request) error {
	return unsupportedContainerEndpoint(r)
}

func (cr *containerRouter) getContainersArchive(w http.ResponseWriter, r *http.Request) error {
	return unsupportedContainerEndpoint(r)
}

type dockerAttachInput struct {
	io.Reader
	io.Closer
}

type dockerFrameWriter struct {
	writer io.Writer
	mu     *sync.Mutex
	stream byte
}

func (frame dockerFrameWriter) Write(data []byte) (int, error) {
	frame.mu.Lock()
	defer frame.mu.Unlock()
	var header [8]byte
	header[0] = frame.stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
	if _, err := frame.writer.Write(header[:]); err != nil {
		return 0, err
	}
	return frame.writer.Write(data)
}

func hijackDockerStreams(w http.ResponseWriter, r *http.Request, tty bool) (io.ReadWriteCloser, io.Reader, io.Writer, io.Writer, error) {
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	contentType := types.MediaTypeRawStream
	if !tty && versions.GreaterThanOrEqualTo(version.VersionFromRequest(r), "1.42") {
		contentType = types.MediaTypeMultiplexedStream
	}
	status := "HTTP/1.1 200 OK\r\n"
	if r.Header.Get("Upgrade") != "" {
		status = "HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n"
	}
	if _, err := fmt.Fprintf(conn, "%sContent-Type: %s\r\n\r\n", status, contentType); err != nil {
		_ = conn.Close()
		return nil, nil, nil, nil, err
	}
	if tty {
		return conn, buffered.Reader, conn, conn, nil
	}
	mu := new(sync.Mutex)
	return conn, buffered.Reader, dockerFrameWriter{writer: conn, mu: mu, stream: 1}, dockerFrameWriter{writer: conn, mu: mu, stream: 2}, nil
}

func (cr *containerRouter) postCommit(w http.ResponseWriter, r *http.Request) error {
	return unsupportedContainerEndpoint(r)
}

func (cr *containerRouter) putContainersArchive(w http.ResponseWriter, r *http.Request) error {
	return unsupportedContainerEndpoint(r)
}

func unsupportedContainerEndpoint(r *http.Request) error {
	return httpx.NewHTTPError(http.StatusNotImplemented, fmt.Errorf("%s %s is not implemented", r.Method, r.URL.Path))
}

func queryBool(r *http.Request, key string) (bool, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, httpx.BadRequest(fmt.Errorf("invalid %s value %q", key, value))
	}
	return parsed, nil
}
