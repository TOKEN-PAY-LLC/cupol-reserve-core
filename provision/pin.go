package provision

// Pinned is the node-install.sh this app build runs: the file at a fixed
// commit of the app's own repository and its SHA-256. TestPinnedScriptHash
// keeps the hash in step with deploy/node-install.sh; when the script
// changes, commit it, then point PinnedCommit at that commit.
const (
	PinnedRepo   = "p1neappleXpress/OpenFlux"
	PinnedCommit = "768745dd7e2ec61034c53dc16f6383029d287e57"
	PinnedSHA256 = "229bd1bcb87930624028aff290b30fdbfdf3f8640666869f830afdff083a0ade"
)

// Pinned returns the script location for this build.
func Pinned() Script {
	return Script{
		URL:    "https://raw.githubusercontent.com/" + PinnedRepo + "/" + PinnedCommit + "/deploy/node-install.sh",
		SHA256: PinnedSHA256,
	}
}
