// Package version exposes the build version shared by all apps in this repo.
// Bump this when cutting a new GitHub release and the self-updater will
// recognize the bump on next launch.
package version

const Version = "0.3.30"

// GitHub repo coordinates for the self-updater.
const (
	RepoOwner = "MiwiDots"
	RepoName  = "streamerchat"
)
