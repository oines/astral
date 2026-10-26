package computers

// Installer commands always bootstrap through the publisher's HTTPS release,
// independently of the deployment's HTTP/HTTPS address. Callers quote all
// deployment-specific argument values before passing the argument string.
func ShellInstallerCommand(arguments string) string {
	return "curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' " + shellQuote("https://github.com/oines/astral/releases/latest/download/install-computer.sh") + " | sh -s -- " + arguments
}

func PowerShellInstallerCommand(arguments string) string {
	// Modern Windows includes curl.exe. Check its exit status before parsing
	// any returned source so failed/truncated downloads are never executed.
	return "& { $astralInstaller = & curl.exe --fail --silent --show-error --location --proto '=https' --proto-redir '=https' " + powerShellQuote("https://github.com/oines/astral/releases/latest/download/install-computer.ps1") + "; if ($LASTEXITCODE -ne 0 -or -not $astralInstaller) { throw 'Astral installer download failed.' }; & ([scriptblock]::Create(($astralInstaller -join \"`n\"))) " + arguments + " }"
}
