package platform

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// A provisioning/update operation keeps the CLI and its environment together
// with the verified endpoint. No process-global cache is used: later operations
// may intentionally select a different installation or Docker context.
type dockerExecution struct {
	executable string
	env        []string
	err        error
}

type dockerExecutionKey struct{}

func currentDockerExecution() *dockerExecution {
	executable, err := dockerExecutable()
	return &dockerExecution{
		executable: executable,
		env:        dockerEnvironment(runtime.GOOS, os.Environ(), executable, filepath.EvalSymlinks),
		err:        err,
	}
}

func dockerExecutionFor(ctx context.Context, info DockerInfo) *dockerExecution {
	if info.execution != nil {
		return info.execution
	}
	if execution, ok := ctx.Value(dockerExecutionKey{}).(*dockerExecution); ok {
		return execution
	}
	if target, ok := ctx.Value(dockerTargetKey{}).(DockerInfo); ok && target.execution != nil {
		return target.execution
	}
	return currentDockerExecution()
}

// GUI environments can omit both the Docker CLI directory and the credential
// helpers shipped alongside it. Append only this installation's directories,
// preserving explicit PATH/helper selections and the CLI's multicall alias.
func dockerEnvironment(goos string, inherited []string, executable string, resolve func(string) (string, error)) []string {
	key, value := "PATH", dockerEnvironmentValue(goos, inherited, "PATH")
	for _, entry := range inherited {
		name, _, _ := strings.Cut(entry, "=")
		if dockerEnvironmentKey(goos, name, "PATH") {
			key = name
		}
	}
	separator := ":"
	if goos == "windows" {
		separator = ";"
	}
	entries := strings.Split(value, separator)
	appendDirectory := func(program string) {
		if dockerExecutableCandidate(goos, program) == "" {
			return
		}
		directory := path.Dir(program)
		if goos == "windows" {
			normal := strings.ReplaceAll(program, `\`, "/")
			end := strings.LastIndex(normal, "/")
			if end == 2 && normal[1] == ':' {
				end++ // Keep the slash of a Windows drive root.
			}
			directory = strings.ReplaceAll(normal[:end], "/", `\`)
		}
		for _, entry := range entries {
			left, right := entry, directory
			if goos == "windows" {
				left = strings.ReplaceAll(strings.Trim(left, `"`), `\`, "/")
				right = strings.ReplaceAll(right, `\`, "/")
				if strings.EqualFold(path.Clean(left), path.Clean(right)) {
					return
				}
			} else if path.Clean(left) == path.Clean(right) {
				return
			}
		}
		if value == "" {
			value = directory
		} else {
			value += separator + directory
		}
		entries = append(entries, directory)
	}
	appendDirectory(executable)
	if resolve != nil {
		if resolved, err := resolve(executable); err == nil {
			appendDirectory(resolved)
		}
	}
	result := make([]string, 0, len(inherited)+1)
	for _, entry := range inherited {
		name, _, _ := strings.Cut(entry, "=")
		if !dockerEnvironmentKey(goos, name, "PATH") {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}

func dockerEnvironmentKey(goos, a, b string) bool {
	return a == b || goos == "windows" && strings.EqualFold(a, b)
}

func dockerEnvironmentValue(goos string, env []string, name string) string {
	var value string
	for _, entry := range env {
		key, next, _ := strings.Cut(entry, "=")
		if dockerEnvironmentKey(goos, key, name) {
			value = next
		}
	}
	return value
}

func fixedDockerEnvironment(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "BUILDX_BUILDER", "BUILDKIT_HOST":
			continue
		}
		result = append(result, entry)
	}
	return result
}
