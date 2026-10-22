package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// hashCanonical receives only canonical Build output. An explicit envelope avoids
// including the hash itself or future diagnostic/provenance fields accidentally.
func hashCanonical(p Plan) string {
	data, err := json.Marshal(struct {
		SchemaVersion string `json:"schemaVersion"`
		Nodes         []Node `json:"nodes"`
	}{p.SchemaVersion, p.Nodes})
	if err != nil {
		panic(err)
	} // This concrete string/slice-only schema cannot fail to encode.
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
