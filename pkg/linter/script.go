// Copyright © 2021 The Tekton Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package linter

import (
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/tektoncd/catlin/pkg/parser"
	"github.com/tektoncd/catlin/pkg/validator"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
)

type taskLinter struct {
	res     *parser.Resource
	configs []config
}

type linter struct {
	cmd  string
	args []string
}

type config struct {
	regexp  string
	linters []linter
}

// NewConfig construct default config
func NewConfig() []config {
	return []config{
		// Default one is the first one
		{
			regexp: `(/usr/bin/env |.*/bin/)sh`,
			linters: []linter{
				{
					cmd:  "shellcheck",
					args: []string{"-s", "sh"},
				},
				{
					cmd:  "sh",
					args: []string{"-n"},
				},
			},
		},
		{
			regexp: `(/usr/bin/env |.*/bin/)bash`,
			linters: []linter{
				{
					cmd:  "shellcheck",
					args: []string{"-s", "bash"},
				},
				{
					cmd:  "bash",
					args: []string{"-n"},
				},
			},
		},
		{
			regexp: `(/usr/bin/env\s|.*/bin/|/usr/libexec/platform-)python(23)?`,
			linters: []linter{
				{
					cmd:  "pylint",
					args: []string{"-dC0103"}, // Disabling C0103 which is invalid name convention
				},
			},
		},
	}
}

// NewScriptLinter construct a new task lister struct
func NewScriptLinter(r *parser.Resource) *taskLinter {
	return &taskLinter{res: r, configs: NewConfig()}
}

// nolint: staticcheck
func (t *taskLinter) validateScript(taskName string, script string, configs []config, stepName string) validator.Result {
	result := validator.Result{}

	// use /bin/sh by default if no shbang
	if script[0:2] != "#!" {
		script = "#!/usr/bin/env sh\n" + script
	} else { // using a shbang, check if we have /usr/bin/env
		if script[0:14] != "#!/usr/bin/env" {
			result.Warn("step: %s is not using #!/usr/bin/env ", taskName)
		}
	}

	for _, config := range configs {
		matched, err := regexp.MatchString(`^#!`+config.regexp+`\n`, script)
		if err != nil {
			result.Error("Invalid regexp: %s", config.regexp)
			return result
		}

		if !matched {
			continue
		}

		for _, linter := range config.linters {
			execpath, err := exec.LookPath(linter.cmd)
			if err != nil {
				result.Error("Couldn't find the linter %s in the path", linter.cmd)
				return result
			}
			tmpfile, err := os.CreateTemp("", "catlin-script-linter")
			if err != nil {
				result.Error("Cannot create temporary files")
				return result
			}
			defer os.Remove(tmpfile.Name()) // clean up
			if _, err := tmpfile.Write([]byte(script)); err != nil {
				result.Error("Cannot write to temporary files")
				return result
			}
			if err := tmpfile.Close(); err != nil {
				result.Error("Cannot close temporary files")
				return result
			}

			// TODO: perhaps the filename is not necessary will be at the end of
			// a command, may need some variable interpolation so the linter can
			// specify where the filaname is into the command line.
			cmd := exec.Command(execpath, append(linter.args, tmpfile.Name())...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				outt := strings.ReplaceAll(string(out), tmpfile.Name(), taskName+"-"+stepName)
				result.Error("%s, %s failed:\n%s", execpath, linter.args, outt)
			}
		}
	}

	return result
}

// nolint: staticcheck
func (t *taskLinter) collectOverSteps(steps interface{}, name string, result *validator.Result) {
	if s, ok := steps.([]v1beta1.Step); ok {
		for _, step := range s {
			if step.Script != "" {
				result.Append(t.validateScript(name, step.Script, t.configs, step.Name))
			}
		}
	} else if s, ok := steps.([]v1.Step); ok {
		for _, step := range s {
			if step.Script != "" {
				result.Append(t.validateScript(name, step.Script, t.configs, step.Name))
			}
		}
	}
}

// nolint: staticcheck
func (t *taskLinter) Validate() validator.Result {
	result := validator.Result{}
	res, err := t.res.ToType()
	if err != nil {
		result.Error("Failed to decode to a Task - %s", err)
		return result
	}

	switch strings.ToLower(t.res.Kind) {
	case "task":
		if res.(*v1.Task) != nil {
			task := res.(*v1.Task)
			t.collectOverSteps(task.Spec.Steps, task.ObjectMeta.Name, &result)
		} else {
			task := res.(*v1beta1.Task)
			t.collectOverSteps(task.Spec.Steps, task.ObjectMeta.Name, &result)
		}

	case "clustertask":
		task := res.(*v1beta1.ClusterTask)
		t.collectOverSteps(task.Spec.Steps, task.ObjectMeta.Name, &result)
	}
	return result
}
