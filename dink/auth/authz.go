package auth

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sysson/dink/pkg/httputils"
	"github.com/sysson/dink/pkg/ioutils"
	"github.com/sysson/dink/pkg/tlsutils"
	authv1 "github.com/sysson/dink/sdk/auth/v1"
)

const maxBodySize = 4 * 1024 * 1024

func NewCtx(authZPlugins *AuthChain, req *Request) *Ctx {
	return &Ctx{
		plugins: authZPlugins.plugins,
		Request: *req,
	}
}

type Request struct {
	Namespace       string
	User            string
	UserAuthNMethod string
	RequestMethod   string
	RequestURI      string
}

type Ctx struct {
	Request
	plugins []plugin
	authReq *authv1.AuthZReqRequest
	authRes *authv1.AuthZResRequest
}

func (ctx *Ctx) AuthZRequest(w http.ResponseWriter, r *http.Request) error {
	var body []byte
	if sendBody(ctx.RequestURI, r.Header) {
		bufBody := bufio.NewReaderSize(r.Body, maxBodySize+1)
		r.Body = ioutils.NewReadCloserWrapper(bufBody, r.Body.Close)

		peeked, err := bufBody.Peek(maxBodySize + 1)
		if err == nil {
			return fmt.Errorf("request body too large for authorization plugin: size exceeds %d bytes", maxBodySize)
		} else if err != io.EOF {
			return err
		}

		body = peeked
	}

	var h bytes.Buffer
	if err := r.Header.Write(&h); err != nil {
		return err
	}

	ctx.authReq = &authv1.AuthZReqRequest{
		Namespace:       ctx.Namespace,
		User:            ctx.User,
		UserAuthNMethod: ctx.UserAuthNMethod,
		RequestMethod:   ctx.RequestMethod,
		RequestURI:      ctx.RequestURI,
		RequestBody:     body,
		RequestHeaders:  headers(r.Header),
	}

	if r.TLS != nil {
		for _, c := range r.TLS.PeerCertificates {
			pc := tlsutils.PeerCertificate(*c)
			b, err := pc.MarshalJSON()
			if err != nil {
				return err
			}
			ctx.authReq.RequestPeerCertificates = append(ctx.authReq.RequestPeerCertificates, b)
		}
	}

	for _, plugin := range ctx.plugins {

		authRes, err := plugin.client.AuthZReq(r.Context(), ctx.authReq)
		if err != nil {
			return fmt.Errorf("plugin %s failed with error: %s", plugin.name, err)
		}

		if !authRes.Allow {
			return httputils.Forbidden(fmt.Errorf("authorization denied by plugin %s: %s", plugin.name, authRes.Msg))
		}
	}

	return nil
}

func (ctx *Ctx) AuthZResponse(rm ioutils.ResponseModifier, r *http.Request) error {
	ctx.authRes = &authv1.AuthZResRequest{
		Namespace:               ctx.authReq.Namespace,
		User:                    ctx.authReq.User,
		UserAuthNMethod:         ctx.authReq.UserAuthNMethod,
		RequestMethod:           ctx.authReq.RequestMethod,
		RequestURI:              ctx.authReq.RequestURI,
		RequestBody:             ctx.authReq.RequestBody,
		RequestHeaders:          ctx.authReq.RequestHeaders,
		RequestPeerCertificates: ctx.authReq.RequestPeerCertificates,
		ResponseBody:            rm.RawBody(),
		ResponseHeaders:         headers(rm.Header()),
		ResponseStatusCode:      int32(rm.StatusCode()),
	}

	if sendBody(ctx.RequestURI, rm.Header()) {
		ctx.authRes.ResponseBody = rm.RawBody()
	}
	for _, plugin := range ctx.plugins {

		authRes, err := plugin.client.AuthZRes(r.Context(), ctx.authRes)
		if err != nil {
			return fmt.Errorf("plugin %s failed with error: %s", plugin.name, err)
		}

		if !authRes.Allow {
			return httputils.Forbidden(fmt.Errorf("authorization denied by plugin %s: %s", plugin.name, authRes.Msg))
		}
	}

	_ = rm.FlushAll()

	return nil
}

func isAuthEndpoint(urlPath string) (bool, error) {
	matched, err := regexp.MatchString(`^[^\/]*\/(v\d[\d\.]*\/)?auth.*`, urlPath)
	if err != nil {
		return false, err
	}
	return matched, nil
}

func sendBody(inURL string, header http.Header) bool {
	u, err := url.Parse(inURL)
	if err != nil {
		return false
	}

	isAuth, err := isAuthEndpoint(u.Path)
	if isAuth || err != nil {
		return false
	}

	contentType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		return false
	}

	return contentType == "application/json"
}

func headers(header http.Header) map[string]string {
	v := make(map[string]string)
	for k, values := range header {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "X-Registry-Config") || strings.EqualFold(k, "X-Registry-Auth") {
			continue
		}
		for _, val := range values {
			v[k] = val
		}
	}
	return v
}
