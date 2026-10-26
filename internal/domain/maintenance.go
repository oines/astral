package domain

import "time"

// ComputerMaintenance describes commands to run on the Computer's host. It does
// not claim that a remote operating-system service was inspected by the Server.
type ComputerMaintenance struct {
	Manager           string       `json:"manager" enum:"desktop,docker,launchd,systemd-user,task-scheduler,unknown"`
	Command           string       `json:"command,omitempty"`
	PowerShellCommand string       `json:"powershell_command,omitempty"`
	CanUpdate         bool         `json:"can_update"`
	ServerNewer       bool         `json:"server_newer"`
	DaemonNewer       bool         `json:"daemon_newer"`
	Update            DaemonUpdate `json:"update"`
}

type DaemonUpdate struct {
	State     string    `json:"state" enum:",checking,downloading,restarting,current,complete,failed"`
	Version   string    `json:"version,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}
