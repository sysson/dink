package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociref"
	"github.com/sysson/syskit/stream"
)

func TestCopyBlobsStopsAfterUploadFailure(t *testing.T) {
	want := errors.New("upload failed")
	destination := &oci.Funcs{
		ResolveBlob_: func(context.Context, string, oci.Digest) (oci.Descriptor, error) {
			return oci.Descriptor{}, oci.ErrBlobUnknown
		},
		PushBlob_: func(context.Context, string, oci.Descriptor, io.Reader) (oci.Descriptor, error) {
			return oci.Descriptor{}, want
		},
	}
	var descriptors []oci.Descriptor
	for range 20 {
		descriptors = append(descriptors, oci.Descriptor{Digest: ocidigest.FromBytes([]byte("blob")), Data: []byte("blob"), Size: 4})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	copier := &copier{destination: destination, pushing: true}
	if _, err := copier.copyBlobs(ctx, blobsFromDescriptors(descriptors)); !errors.Is(err, want) {
		t.Fatalf("upload error = %v, want %v", err, want)
	}
}

func TestPushProgressOnlyShowsLayers(t *testing.T) {
	for _, mediaType := range []string{"application/vnd.oci.image.layer.v1.tar+gzip", oci.MediaTypeImageConfig, "application/vnd.in-toto+json"} {
		for _, cached := range []bool{false, true} {
			for _, inline := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/cached=%v/inline=%v", mediaType, cached, inline), func(t *testing.T) {
					data := []byte("content")
					desc := oci.Descriptor{MediaType: mediaType, Digest: ocidigest.FromBytes(data), Size: int64(len(data))}
					source := ocimem.New()
					if _, err := source.PushBlob(context.Background(), "source", desc, bytes.NewReader(data)); err != nil {
						t.Fatal(err)
					}
					destination := ocimem.New()
					if cached {
						if _, err := destination.PushBlob(context.Background(), "target", desc, bytes.NewReader(data)); err != nil {
							t.Fatal(err)
						}
					}
					if inline {
						desc.Data = data
					}
					var progress bytes.Buffer
					copier := &copier{
						client: source, destination: destination,
						srcRef: ociref.Reference{Repository: "source"}, dstRef: ociref.Reference{Repository: "target"},
						out: stream.NewJSONProgressOutput(&progress, true), pushing: true,
					}
					if _, err := copier.copyBlob(context.Background(), desc); err != nil {
						t.Fatal(err)
					}
					if _, err := destination.ResolveBlob(context.Background(), "target", desc.Digest); err != nil {
						t.Fatalf("hidden progress must not skip upload: %v", err)
					}
					if isImageLayer(mediaType) {
						status := "Pushed"
						if cached {
							status = "Already exists"
						}
						if !strings.Contains(progress.String(), status) {
							t.Fatalf("missing layer status %s: %s", status, progress.String())
						}
					} else if progress.Len() != 0 {
						t.Fatalf("non-layer progress: %s", progress.String())
					}
				})
			}
		}
	}
}
