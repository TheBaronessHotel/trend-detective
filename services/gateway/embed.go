// Package gateway holds the gateway's embedded files. The service itself is
// in cmd/gateway and internal/.
package gateway

import _ "embed"

// CatalogYAML is catalog.yaml, built into the binary.
//
//go:embed catalog.yaml
var CatalogYAML []byte
