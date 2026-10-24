package domain

// ComputerMaintenance describes commands to run on the Computer's host. It does
// not claim that a remote operating-system service was inspected by the Server.
type ComputerMaintenance struct {
	Manager           string `json:"manager" enum:"desktop,docker,launchd,systemd-user,task-scheduler,unknown"`
	Command           string `json:"command,omitempty"`
	PowerShellCommand string `json:"powershell_command,omitempty"`
}
