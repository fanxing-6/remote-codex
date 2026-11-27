package protocol

const Version = 1
const MaxFrame = 16 << 20
const MaxQueue = 32 << 20
const MaxPending = 256
const MaxQueuedFrames = 8192
const TakenOver = 4009
const HostUnavailable = 4010

var Channels = []string{"connect-app-host", "codex_desktop:connect-app-server-manager", "codex_desktop:git-worker-port"}

type Envelope struct {
	Kind         string `json:"kind"`
	ID           string `json:"id,omitempty"`
	Generation   uint64 `json:"generation,omitempty"`
	AssetBuildID string `json:"assetBuildId,omitempty"`
}
