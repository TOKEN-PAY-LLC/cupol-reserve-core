package provision

// Pinned is the node-install.sh this app build runs: the file at a fixed
// commit of the app's own repository and its SHA-256. TestPinnedScriptHash
// keeps the hash in step with deploy/node-install.sh; when the script
// changes, commit it, then point PinnedCommit at that commit.
const (
	PinnedRepo   = "TOKEN-PAY-LLC/cupol-reserve-core"
	PinnedCommit = "6bc869f17235ce5209e830d18187890270a835e1"
	PinnedSHA256 = "e350da69f61e2ef86e4c2b2bc55671b8bc4e78547923aac50a1adea2d91f854d"
)

// Pinned returns the script location for this build.
func Pinned() Script {
	return Script{
		URL:    "https://raw.githubusercontent.com/" + PinnedRepo + "/" + PinnedCommit + "/deploy/node-install.sh",
		SHA256: PinnedSHA256,
	}
}
