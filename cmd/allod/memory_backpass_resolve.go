package main

// A bare run resolves each input by option, env, registry, then convention.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type resolvedInput struct {
	path, source string
}

// A fatal refuses at once; a non-empty result wins; tried is returned either
// way so a refusal can list every place checked.
type resolveStep func() (results []resolvedInput, tried string, fatal error)

func resolveFirst(steps ...resolveStep) (results []resolvedInput, tried []string, err error) {
	for _, step := range steps {
		r, desc, fatal := step()
		tried = append(tried, desc)
		if fatal != nil {
			return nil, tried, fatal
		}
		if len(r) > 0 {
			return r, tried, nil
		}
	}
	return nil, tried, nil
}

func isExistingDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func wrapInputs(paths []string, source string) []resolvedInput {
	out := make([]resolvedInput, len(paths))
	for i, p := range paths {
		out[i] = resolvedInput{p, source}
	}
	return out
}

func tracesResolveSteps(flagDirs []string) []resolveStep {
	return []resolveStep{
		func() ([]resolvedInput, string, error) {
			if len(flagDirs) == 0 {
				return nil, "--traces", nil
			}
			return wrapInputs(flagDirs, "option"), "--traces", nil
		},
		func() ([]resolvedInput, string, error) {
			v := os.Getenv("ALLOD_BACKPASS_TRACES")
			if v == "" {
				return nil, "ALLOD_BACKPASS_TRACES", nil
			}
			var paths []string
			for _, p := range strings.Split(v, ":") {
				if p != "" {
					paths = append(paths, p)
				}
			}
			return wrapInputs(paths, "env"), "ALLOD_BACKPASS_TRACES", nil
		},
		func() ([]resolvedInput, string, error) {
			var out []resolvedInput
			var checked []string
			for _, id := range tracesRegistryIds() {
				checkout, ok := registryCheckout(id)
				if !ok {
					continue
				}
				dir := filepath.Join(workDir(), checkout)
				checked = append(checked, dir)
				if isExistingDir(dir) {
					out = append(out, resolvedInput{dir, "registry " + id})
				}
			}
			desc := `registry entries marked "traces": true`
			if len(checked) > 0 {
				desc += " (" + strings.Join(checked, ", ") + ")"
			}
			return out, desc, nil
		},
		func() ([]resolvedInput, string, error) {
			host, _ := os.Hostname()
			dir := filepath.Join(workDir(), "traces-"+host)
			if isExistingDir(dir) {
				return []resolvedInput{{dir, "convention"}}, dir, nil
			}
			return nil, dir, nil
		},
		func() ([]resolvedInput, string, error) {
			dir := filepath.Join(workDir(), "traces")
			if isExistingDir(dir) {
				return []resolvedInput{{dir, "convention"}}, dir, nil
			}
			return nil, dir, nil
		},
	}
}

func privateResolveSteps(flagDir string, flagSet bool) []resolveStep {
	return []resolveStep{
		func() ([]resolvedInput, string, error) {
			if !flagSet {
				return nil, "--private", nil
			}
			return []resolvedInput{{flagDir, "option"}}, "--private", nil
		},
		func() ([]resolvedInput, string, error) {
			v := os.Getenv("ALLOD_BACKPASS_PRIVATE")
			if v == "" {
				return nil, "ALLOD_BACKPASS_PRIVATE", nil
			}
			return []resolvedInput{{v, "env"}}, "ALLOD_BACKPASS_PRIVATE", nil
		},
		func() ([]resolvedInput, string, error) {
			var ids []string
			for _, id := range memoryRegistryIds() {
				if id != "allod/memory" {
					ids = append(ids, id)
				}
			}
			switch len(ids) {
			case 0:
				return nil, `registry entries marked "memory": true (other than allod/memory)`, nil
			case 1:
				checkout, ok := registryCheckout(ids[0])
				if !ok {
					return nil, ids[0], fmt.Errorf("registry entry %s has no checkout path", ids[0])
				}
				return []resolvedInput{{filepath.Join(workDir(), checkout), "registry " + ids[0]}}, ids[0], nil
			default:
				return nil, strings.Join(ids, ", "), fmt.Errorf(`multiple private memory registry entries found (%s); pass --private`, strings.Join(ids, ", "))
			}
		},
		func() ([]resolvedInput, string, error) {
			dir := filepath.Join(workDir(), "agent-memory")
			if isExistingDir(dir) {
				return []resolvedInput{{dir, "convention"}}, dir, nil
			}
			return nil, dir, nil
		},
	}
}

// The registry-default step always returns a path, so this never falls through.
func publicResolveSteps(flagDir string, flagSet bool) []resolveStep {
	return []resolveStep{
		func() ([]resolvedInput, string, error) {
			if !flagSet {
				return nil, "--public", nil
			}
			return []resolvedInput{{flagDir, "option"}}, "--public", nil
		},
		func() ([]resolvedInput, string, error) {
			v := os.Getenv("ALLOD_BACKPASS_PUBLIC")
			if v == "" {
				return nil, "ALLOD_BACKPASS_PUBLIC", nil
			}
			return []resolvedInput{{v, "env"}}, "ALLOD_BACKPASS_PUBLIC", nil
		},
		func() ([]resolvedInput, string, error) {
			checkout, ok := registryCheckout("allod/memory")
			if ok {
				return []resolvedInput{{filepath.Join(workDir(), checkout), "registry allod/memory"}}, "allod/memory", nil
			}
			return []resolvedInput{{filepath.Join(workDir(), "allod/memory"), "convention"}}, "allod/memory", nil
		},
	}
}
