package computers

// Installer commands always bootstrap through the publisher's HTTPS release,
// independently of the deployment's HTTP/HTTPS address. Callers quote all
// deployment-specific argument values before passing the argument string.
func ShellInstallerCommand(arguments string) string {
	return "curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \"${ASTRAL_GITHUB_PROXY:+${ASTRAL_GITHUB_PROXY%/}/}https://github.com/oines/astral/releases/latest/download/install-computer.sh\" | sh -s -- " + arguments
}

func PowerShellInstallerCommand(arguments string) string {
	// Modern Windows includes curl.exe. Check its exit status before parsing
	// any returned source so failed/truncated downloads are never executed.
	return "& { $astralInstallerUrl = " + powerShellQuote("https://github.com/oines/astral/releases/latest/download/install-computer.ps1") + "; if ($env:ASTRAL_GITHUB_PROXY) { $astralInstallerUrl = $env:ASTRAL_GITHUB_PROXY.TrimEnd('/') + '/' + $astralInstallerUrl }; $astralInstaller = & curl.exe --fail --silent --show-error --location --proto '=https' --proto-redir '=https' $astralInstallerUrl; if ($LASTEXITCODE -ne 0 -or -not $astralInstaller) { throw 'Astral installer download failed.' }; & ([scriptblock]::Create(($astralInstaller -join \"`n\"))) " + arguments + " }"
}
