package identity

import (
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

func DockerIDFromUID(uid types.UID) string {
	str := strings.ReplaceAll(string(uid), "-", "")
	return str + str
}

func UIDFromDockerID(dockerID string) types.UID {
	if len(dockerID)%2 != 0 {
		return types.UID("")
	}
	half := len(dockerID) / 2
	uidStr := dockerID[:half]
	uidStr = strings.Join([]string{
		uidStr[:8],
		uidStr[8:12],
		uidStr[12:16],
		uidStr[16:20],
		uidStr[20:],
	}, "-")
	return types.UID(uidStr)
}

func Truncate(id string) string {
	if i := strings.IndexRune(id, ':'); i >= 0 {
		id = id[i+1:]
	}
	const shortLen = 12
	if len(id) > shortLen {
		id = id[:shortLen]
	}
	return id
}
