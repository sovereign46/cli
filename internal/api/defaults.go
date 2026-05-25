package api

// DefaultSession returns a baseline Session populated from the team's
// settings. It is used by the session service when a session record is
// not known locally and the client needs to assemble a default payload
// to send to the API. It deliberately leaves transient fields (ID,
// State, Location, Age, Spent, Task) empty.
func DefaultSession(team Team) Session {
	return Session{
		Harness: team.DefaultHarness(),
		Region:  team.Region,
		Model:   team.DefaultModel,
	}
}

// DefaultHarness returns the default harness name the CLI assumes when
// no explicit harness is configured for a team.
func (t Team) DefaultHarness() string {
	return "claude-code"
}
