package main

import (
	"bytes"
	"encoding/json"
	"github.com/onsi/gomega"
	"strings"
	"testing"
)

// Extraction must select exactly the requested version, not a neighboring
// served version or an unrelated CRD from the complete chart render.
func TestExtractExactVersion(t *testing.T) {
	g := gomega.NewWithT(t)
	input := `apiVersion: v1
kind: Service
---
kind: CustomResourceDefinition
spec:
  group: example.org
  names: {kind: Demo}
  versions:
    - name: v1
      schema: {openAPIV3Schema: {type: object, required: [spec]}}
    - name: v2
      schema: {openAPIV3Schema: {type: string}}
`
	var out bytes.Buffer
	g.Expect(extract(strings.NewReader(input), &out, "example.org", "Demo", "v1")).To(gomega.Succeed())
	var result map[string]any
	g.Expect(json.Unmarshal(out.Bytes(), &result)).To(gomega.Succeed())
	g.Expect(result["type"]).To(gomega.Equal("object"))
	g.Expect(result["required"]).To(gomega.Equal([]any{"spec"}))
	g.Expect(extract(strings.NewReader(input), &bytes.Buffer{}, "example.org", "Demo", "v3")).NotTo(gomega.Succeed())
	g.Expect(extract(strings.NewReader("bad: ["), &bytes.Buffer{}, "example.org", "Demo", "v1")).NotTo(gomega.Succeed())
}
