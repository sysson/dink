package buildkit

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"

	control "github.com/moby/buildkit/api/services/control"
	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const historyNamesAttr = "dink.history.original-image-names"
const historyExporterAttr = "dink.history.exporter-index"

func historyPrefix(id identity.Identity) string {
	return "dink-history-" + tenantRef(id, "") + "-"
}

func historyRef(id identity.Identity, ref string) string {
	return historyPrefix(id) + base64.RawURLEncoding.EncodeToString([]byte(ref))
}

func originalHistoryRef(id identity.Identity, ref string) (string, bool) {
	suffix, ok := strings.CutPrefix(ref, historyPrefix(id))
	if !ok {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(suffix)
	if err != nil || len(decoded) == 0 {
		return "", false
	}
	return string(decoded), true
}

func (g *Gateway) ListenBuildHistory(request *control.BuildHistoryRequest, server control.Control_ListenBuildHistoryServer) error {
	id, err := g.check(server.Context())
	if err != nil {
		return err
	}
	if request.Limit < 0 {
		return status.Error(codes.InvalidArgument, "history limit must not be negative")
	}
	request = proto.Clone(request).(*control.BuildHistoryRequest)
	if request.Ref != "" {
		request.Ref = historyRef(id, request.Ref)
	}
	// Each filter entry is an OR branch. Scope every branch before the backend
	// applies its limit, then recheck every event (including unfiltered live events).
	scope := "ref~=^" + historyPrefix(id)
	if len(request.Filter) == 0 {
		request.Filter = []string{scope}
	} else {
		for i, filter := range request.Filter {
			if strings.TrimSpace(filter) == "" {
				request.Filter[i] = scope
				continue
			}
			reader := csv.NewReader(strings.NewReader(filter))
			fields, err := reader.Read()
			if err != nil {
				return status.Error(codes.InvalidArgument, "invalid history filter")
			}
			if _, err := reader.Read(); !errors.Is(err, io.EOF) {
				return status.Error(codes.InvalidArgument, "invalid history filter")
			}
			for _, field := range fields {
				key := field
				if index := strings.IndexAny(field, "=!~<>"); index >= 0 {
					key = field[:index]
				}
				key = strings.TrimSpace(key)
				if strings.Trim(key, `"`) == "ref" {
					return status.Error(codes.Unimplemented, "history ref filters are not supported; select a build reference directly")
				}
			}
			request.Filter[i] = filter + "," + scope
		}
	}
	ctx, cancel := context.WithCancel(server.Context())
	defer cancel()
	stream, err := g.control.ListenBuildHistory(ctx, request)
	if err != nil {
		return err
	}
	headers, err := stream.Header()
	if err != nil {
		return err
	}
	if err := server.SendHeader(headers); err != nil {
		return err
	}
	defer func() { server.SetTrailer(stream.Trailer()) }()
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if event.Record == nil {
			continue
		}
		ref, owned := originalHistoryRef(id, event.Record.Ref)
		if !owned || request.Ref != "" && event.Record.Ref != request.Ref {
			continue
		}
		event = proto.Clone(event).(*control.BuildHistoryEvent)
		event.Record.Ref = ref
		attrs := event.Record.FrontendAttrs
		names := attrs[historyNamesAttr]
		if names != "" {
			index, err := strconv.Atoi(attrs[historyExporterAttr])
			if err != nil || index < 0 || index >= len(event.Record.Exporters) || event.Record.Exporters[index] == nil {
				return status.Error(codes.Internal, "invalid image exporter in BuildKit history")
			}
			if event.Record.Exporters[index].Attrs == nil {
				event.Record.Exporters[index].Attrs = make(map[string]string)
			}
			event.Record.Exporters[index].Attrs["name"] = names
			if event.Record.ExporterResponse != nil {
				event.Record.ExporterResponse["image.name"] = names
				delete(event.Record.ExporterResponse, "containerimage.config.digest")
			}
		}
		delete(attrs, historyNamesAttr)
		delete(attrs, historyExporterAttr)
		if err := server.Send(event); err != nil {
			return err
		}
	}
}

func (g *Gateway) UpdateBuildHistory(ctx context.Context, request *control.UpdateBuildHistoryRequest) (*control.UpdateBuildHistoryResponse, error) {
	id, err := g.check(ctx)
	if err != nil {
		return nil, err
	}
	if request.Ref == "" {
		return nil, status.Error(codes.InvalidArgument, "a build reference is required")
	}
	request = proto.Clone(request).(*control.UpdateBuildHistoryRequest)
	request.Ref = historyRef(id, request.Ref)
	return g.control.UpdateBuildHistory(ctx, request)
}
