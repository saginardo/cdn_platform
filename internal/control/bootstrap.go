package control

import _ "embed"

// These are the canonical edge deployment resources served by the control plane.
//
//go:embed install-edge.sh
var bootstrapEdgeScript string

//go:embed install-edge.service
var bootstrapEdgeService string
