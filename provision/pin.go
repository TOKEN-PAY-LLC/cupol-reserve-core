package provision

// Pinned is the node-install.sh this app build runs: the file at a fixed
// commit of the app's own repository and its SHA-256. TestPinnedScriptHash
// keeps the hash in step with deploy/node-install.sh; when the script
// changes, commit it, then point PinnedCommit at that commit.
const (
	PinnedRepo   = "TOKEN-PAY-LLC/cupol-reserve-core"
	PinnedCommit = "d11d1004c55747c6624b61ddad0dc0688f4f34b5"
	PinnedSHA256 = "5246ac4c76c293f642e9fb1830e6ecf9ddbd11c0bb0fa397a565505273e8cd90"
)

// Pinned returns the script location for this build.
func Pinned() Script {
	return Script{
		URL:    "https://raw.githubusercontent.com/" + PinnedRepo + "/" + PinnedCommit + "/deploy/node-install.sh",
		SHA256: PinnedSHA256,
	}
}
