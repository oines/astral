package domain

import "slices"

// RootSessionID returns the owning root Session identity for a Session or its
// direct subagent.
func RootSessionID(session Session) string {
	if session.SubagentParentID != "" {
		return session.SubagentParentID
	}
	return session.ID
}

// ComputerAllowedForSession reports whether the given Session may use the given
// Computer. Permanent Computers follow Agent.AllowedComputers; temporary
// Computers are scoped strictly to their owning root Session and its subagents.
func ComputerAllowedForSession(agent Agent, session Session, computer Computer) bool {
	if computer.ID == "" || computer.Revoked {
		return false
	}
	if computer.Temporary {
		if computer.OwnerSessionID == "" || computer.OwnerAgentID == "" {
			return false
		}
		return session.AgentID == computer.OwnerAgentID && RootSessionID(session) == computer.OwnerSessionID
	}
	return slices.Contains(agent.AllowedComputers, computer.ID)
}
