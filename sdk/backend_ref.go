package mesh

import (
	"strconv"
	"strings"
)

func makeBackendRef(publicPort string, index int) string {
	return strings.TrimSpace(publicPort) + ":" + strconv.Itoa(index)
}

func parseBackendRef(ref string) (string, int, bool) {
	ref = strings.TrimSpace(ref)
	parts := strings.Split(ref, ":")
	if len(parts) != 2 {
		return "", 0, false
	}

	publicPort := strings.TrimSpace(parts[0])
	if publicPort == "" {
		return "", 0, false
	}

	index, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || index < 0 {
		return "", 0, false
	}

	return publicPort, index, true
}
