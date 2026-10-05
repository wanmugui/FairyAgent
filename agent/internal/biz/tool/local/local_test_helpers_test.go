package local

import (
	"context"
)

type recordingLocalProcessRunner struct {
	request localProcessRequest
	result  localProcessResult
	err     error
	inspect func(localProcessRequest)
}

func (r *recordingLocalProcessRunner) Run(_ context.Context, request localProcessRequest) (localProcessResult, error) {
	r.request = request
	if r.inspect != nil {
		r.inspect(request)
	}
	return r.result, r.err
}

func localExecuteTestConfig() *Config {
	return &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Python: "/custom/python"}}}
}

func localExecuteTestResolver() localExecutableResolver {
	return resolverForTest("darwin", nil, nil, map[string]bool{"/custom/python": true})
}

func containsLocalEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
