package system

import (
	"net/http"

	"github.com/sysson/dink/pkg/utils/httputils"
)

func optionsHandler(w http.ResponseWriter, r *http.Request) error {
	w.WriteHeader(http.StatusOK)
	return nil
}

func (s *systemRouter) pingHandler(w http.ResponseWriter, r *http.Request) error {
	w.Header().Add("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Add("Pragma", "no-cache")

	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "0")
		return nil
	}
	_, err := w.Write([]byte{'O', 'K'})
	return err
}

func (s *systemRouter) getEvents(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (s *systemRouter) getInfo(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (s *systemRouter) getVersion(w http.ResponseWriter, r *http.Request) error {
	version, err := s.translator.SystemVersion()
	if err != nil {
		return err
	}
	resp := httputils.HTTPResponse{
		StatusCode: http.StatusOK,
		Msg:        version,
	}
	return resp.WriteJSON(w)
}

func (s *systemRouter) getDiskUsage(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (s *systemRouter) postAuth(w http.ResponseWriter, r *http.Request) error {
	return nil
}
