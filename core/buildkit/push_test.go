package buildkit

import (
	"context"
	"slices"
	"testing"

	control "github.com/moby/buildkit/api/services/control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSolvePushSeparatesInternalAndUpstreamOptions(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "upstream failure"}[failure], func(t *testing.T) {
			backend := &solveClient{}
			g, publisher, b, ctx := newSolveGateway(t, backend)
			input := solveRequest()
			const names = "ghcr.io/team/app:test,ghcr.io/team/app:latest"
			input.Exporters[0].Attrs = map[string]string{
				"name": names, "push": "true", "registry.insecure": "false",
				"compression": "gzip", "oci-mediatypes": "true",
			}
			before := proto.Clone(input)
			backend.solve = func(_ context.Context, request *control.SolveRequest) (*control.SolveResponse, error) {
				if len(request.Exporters) != 2 {
					t.Fatalf("expected two exporters: %v", request.Exporters)
				}
				internal, upstream := request.Exporters[0], request.Exporters[1]
				if internal.Type != "image" || internal.Attrs["registry.insecure"] != "true" || internal.Attrs["store"] != "false" ||
					internal.Attrs["name"] != "dinki.test:5000/tenant/app:test,dinki.test:5000/tenant/app:latest" {
					t.Fatalf("incorrect internal export: %v", internal)
				}
				if upstream.Type != "image" || upstream.Attrs["name"] != names || upstream.Attrs["registry.insecure"] != "false" ||
					upstream.Attrs["compression"] != "gzip" || upstream.Attrs["oci-mediatypes"] != "true" ||
					upstream.Attrs["push"] != "true" || upstream.Attrs["store"] != "false" {
					t.Fatalf("incorrect upstream export: %v", upstream)
				}
				if failure {
					return nil, status.Error(codes.PermissionDenied, "upstream denied")
				}
				return &control.SolveResponse{ExporterResponse: map[string]string{"containerimage.digest": "manifest"}}, nil
			}
			response, err := g.Solve(ctx, input)
			if failure {
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("upstream failure not surfaced: %v", err)
				}
			} else if err != nil || response.ExporterResponse["image.name"] != names {
				t.Fatalf("incorrect push response: %v %v", response, err)
			}
			if !proto.Equal(input, before) || !slices.Equal(publisher.names, []string{"ghcr.io/team/app:test", "ghcr.io/team/app:latest"}) ||
				!slices.Equal(publisher.revoked, []string{"build-user"}) || b.credential != nil {
				t.Fatal("mutated caller or retained credentials")
			}
		})
	}
}
