// Package appmeta holds project-wide metadata shared between packages that
// must not depend on each other (e.g. enrichment and lastfm).
package appmeta

// RepoURL is the project repository address. It is the User-Agent contact
// fallback: MusicBrainz and Wikimedia require a UA that can reach the
// operator, so an empty contact falls back to this address instead of an
// unreachable placeholder.
const RepoURL = "https://github.com/lux032/032music-server"
