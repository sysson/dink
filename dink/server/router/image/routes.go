package image

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/moby/moby/api/types/jsonstream"
	"github.com/sysson/syskit/httpx"
)

func (ir *imageRouter) getImagesJSON(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesSearch(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesGet(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesHistory(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesByName(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImageAttestations(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesLoad(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesCreate(w http.ResponseWriter, r *http.Request) error {
	fromImage := r.URL.Query().Get("fromImage")
	if fromImage == "" {
		return httpx.BadRequest(fmt.Errorf("fromImage is required"))
	}
	tag := r.URL.Query().Get("tag")

	w.Header().Set("Content-Type", "application/json")
	flusher, _ := w.(http.Flusher)
	encoder := json.NewEncoder(w)
	progress := func(message jsonstream.Message) {
		if err := encoder.Encode(message); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	_, err := ir.translator.PullImage(r.Context(), fromImage, tag, progress)
	if err != nil {
		progress(jsonstream.Message{
			Error: &jsonstream.Error{Message: err.Error()},
		})
		return nil
	}
	return nil
}

func (ir *imageRouter) postImagesPush(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesTag(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesPrune(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) deleteImages(w http.ResponseWriter, r *http.Request) error {
	return nil
}
