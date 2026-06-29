// Package agent embeds the cross-compiled Linux sampler binary so it can be
// uploaded to the remote host at connect time. Rebuild the binary with
// scripts/build-sampler.sh whenever cmd/sampler changes.
package agent

import _ "embed"

//go:embed sampler-linux-amd64
var SamplerBinary []byte

// RemoteName is the sampler's filename on the remote host. The directory is
// chosen at probe time (StageDir) because /tmp is frequently mounted noexec.
const RemoteName = ".rtaskmgr-sampler"
