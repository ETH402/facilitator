package httpapi

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestOpenAPIDocumentsMerchantAuthFailures(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../openapi/eth402.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	cases := []struct {
		path, method string
		statuses     []string
	}{
		{"/v1/api-keys", "get", []string{"401", "403", "429"}},
		{"/v1/api-keys", "post", []string{"400", "401", "403", "429"}},
		{"/v1/api-keys/{id}", "delete", []string{"400", "401", "403", "404"}},
		{"/v1/me/recipient-change", "post", []string{"400", "401", "403", "409", "429"}},
		{"/v1/me/recipient-change/verify", "post", []string{"400", "401", "403", "404", "409", "429"}},
		{"/merchant/api/recipient-challenge", "post", []string{"400", "401", "403", "404", "409", "429"}},
	}
	for _, test := range cases {
		operation := openAPIOperation(t, spec, test.path, test.method)
		for _, status := range test.statuses {
			if !strings.Contains(operation, `"`+status+`":`) {
				t.Errorf("%s %s does not document %s", test.method, test.path, status)
			}
		}
	}
}

func openAPIOperation(t *testing.T, spec, path, method string) string {
	t.Helper()
	pathMarker := "\n  " + path + ":"
	start := strings.Index(spec, pathMarker)
	if start < 0 {
		t.Fatalf("OpenAPI path %s not found", path)
	}
	pathBlock := spec[start+len(pathMarker):]
	if end := strings.Index(pathBlock, "\n  /"); end >= 0 {
		pathBlock = pathBlock[:end]
	}
	methodPattern := regexp.MustCompile(`(?m)^    (get|post|put|delete):`)
	matches := methodPattern.FindAllStringIndex(pathBlock, -1)
	for index, match := range matches {
		if strings.TrimSuffix(strings.TrimSpace(pathBlock[match[0]:match[1]]), ":") != method {
			continue
		}
		end := len(pathBlock)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		return pathBlock[match[0]:end]
	}
	t.Fatalf("OpenAPI operation %s %s not found", method, path)
	return ""
}
