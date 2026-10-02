package buildkit

import (
	"context"
	"errors"
	"io"
	"slices"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	control "github.com/moby/buildkit/api/services/control"
	"github.com/opencontainers/go-digest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type historyContent struct {
	contentapi.UnimplementedContentServer
	gateway *Gateway
}

func (c *historyContent) authorize(ctx context.Context, value string) error {
	id, err := c.gateway.check(ctx)
	if err != nil {
		return err
	}
	if err := digest.Digest(value).Validate(); err != nil {
		return status.Error(codes.InvalidArgument, "a valid history content digest is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.gateway.control.ListenBuildHistory(ctx, &control.BuildHistoryRequest{
		EarlyExit: true,
		Filter:    []string{"ref~=^" + historyPrefix(id)},
	})
	if err != nil {
		return err
	}
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return status.Error(codes.PermissionDenied, "content is not referenced by this client's build history")
		}
		if err != nil {
			return err
		}
		if event.Record == nil {
			continue
		}
		if _, owned := originalHistoryRef(id, event.Record.Ref); !owned {
			continue
		}
		if historyReferencesDigest(event.Record, value) {
			return nil
		}
	}
}

func historyReferencesDigest(record *control.BuildHistoryRecord, value string) bool {
	matches := func(descriptor *control.Descriptor) bool {
		return descriptor != nil && descriptor.Digest == value
	}
	if matches(record.Logs) || matches(record.Trace) || matches(record.ExternalError) {
		return true
	}
	resultMatches := func(result *control.BuildResultInfo) bool {
		if result == nil {
			return false
		}
		if matches(result.ResultDeprecated) {
			return true
		}
		if slices.ContainsFunc(result.Attestations, matches) {
			return true
		}
		for _, descriptor := range result.Results {
			if matches(descriptor) {
				return true
			}
		}
		return false
	}
	if resultMatches(record.Result) {
		return true
	}
	for _, result := range record.Results {
		if resultMatches(result) {
			return true
		}
	}
	return false
}

func (c *historyContent) Info(ctx context.Context, request *contentapi.InfoRequest) (*contentapi.InfoResponse, error) {
	if err := c.authorize(ctx, request.Digest); err != nil {
		return nil, err
	}
	response, err := contentapi.NewContentClient(c.gateway.conn).Info(ctx, request)
	if err != nil {
		return nil, err
	}
	response = proto.Clone(response).(*contentapi.InfoResponse)
	if response.Info != nil {
		// Backend GC labels can name records belonging to other clients.
		response.Info.Labels = nil
	}
	return response, nil
}

func (c *historyContent) Read(request *contentapi.ReadContentRequest, server contentapi.Content_ReadServer) error {
	if err := c.authorize(server.Context(), request.Digest); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(server.Context())
	defer cancel()
	stream, err := contentapi.NewContentClient(c.gateway.conn).Read(ctx, request)
	if err != nil {
		return err
	}
	return relayResponses(stream, server)
}
