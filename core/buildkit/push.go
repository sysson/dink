package buildkit

import (
	"strings"

	"github.com/docker/oci/ociref"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validatePushNames(names, internalHost string) error {
	for name := range strings.SplitSeq(names, ",") {
		ref, err := ociref.ParseRelative(name)
		if err != nil || ref.Tag == "" || ref.Digest != "" {
			return status.Error(codes.InvalidArgument, "push output must be a tagged image reference")
		}
		if strings.EqualFold(ref.Host, internalHost) {
			return status.Error(codes.InvalidArgument, "--push must target an upstream registry, not Dinki's internal endpoint")
		}
	}
	return nil
}
