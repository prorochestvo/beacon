package internal

// The project's outbound identity, declared once for the whole project.
//
// This was previously spelled four times in four packages — the weather client, the
// audit fetcher, the health-check inspector, and a bare literal inside the rate
// extractor — each free to drift from the others. Upstreams see one project, so
// there is one string, composed from parts rather than repeated whole.
//
// The value reaches an upstream. Changing it changes what a rate source or Open-Meteo
// sees, which is a behaviour change and not the point of having a constant: consolidate
// where it is written, keep what is written identical.
//
// Keep this file stdlib-only, for the same reason env.go is: everything imports
// internal, so a dependency here invites a cycle.
const (
	// projectName and projectVersion are unexported because nothing outside this file
	// should assemble its own variant. Add a constant here instead.
	projectName    = "Beacon"
	projectVersion = "1.0"

	// ProjectURL is the contact address in the User-Agent comment, so an upstream
	// operator who wants to complain about traffic has somewhere to go.
	ProjectURL = "https://github.com/seilbekskindirov/beacon"

	// UserAgent identifies ordinary outbound traffic: rate extraction, source audits,
	// weather observations.
	UserAgent = projectName + "/" + projectVersion + " (+" + ProjectURL + ")"

	// HealthCheckUserAgent identifies readiness probes, which are the same client
	// hitting the same upstreams on a schedule nobody asked for. Distinguishing them
	// lets an upstream rate-limit or exclude probe traffic without touching the real
	// requests, and lets us read their logs apart from ours.
	HealthCheckUserAgent = projectName + "/" + projectVersion + " health-check (+" + ProjectURL + ")"
)
