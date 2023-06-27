// Copyright 2022 Woodpecker Authors
// Copyright 2018 Drone.IO Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
	"github.com/rs/zerolog/log"

	backend_types "github.com/woodpecker-ci/woodpecker/pipeline/backend/types"
	yaml_types "github.com/woodpecker-ci/woodpecker/pipeline/frontend/yaml/types"
	forge_types "github.com/woodpecker-ci/woodpecker/server/forge/types"

	"github.com/woodpecker-ci/woodpecker/pipeline/frontend"
	"github.com/woodpecker-ci/woodpecker/pipeline/frontend/metadata"
	"github.com/woodpecker-ci/woodpecker/pipeline/frontend/yaml"
	"github.com/woodpecker-ci/woodpecker/pipeline/frontend/yaml/compiler"
	"github.com/woodpecker-ci/woodpecker/pipeline/frontend/yaml/linter"
	"github.com/woodpecker-ci/woodpecker/pipeline/frontend/yaml/matrix"
	"github.com/woodpecker-ci/woodpecker/server"
	"github.com/woodpecker-ci/woodpecker/server/model"
)

// StepBuilder Takes the hook data and the yaml and returns in internal data model
type StepBuilder struct {
	Repo  *model.Repo
	Curr  *model.Pipeline
	Last  *model.Pipeline
	Netrc *model.Netrc
	Secs  []*model.Secret
	Regs  []*model.Registry
	Link  string
	Yamls []*forge_types.FileMeta
	Envs  map[string]string
	Forge metadata.ServerForge
}

type Item struct {
	Workflow  *model.Workflow
	Platform  string
	Labels    map[string]string
	DependsOn []string
	RunsOn    []string
	Config    *backend_types.Config
}

func (b *StepBuilder) Build() ([]*Item, error) {
	var items []*Item

	b.Yamls = forge_types.SortByName(b.Yamls)

	pidSequence := 1

	for _, y := range b.Yamls {
		// matrix axes
		axes, err := matrix.ParseString(string(y.Data))
		if err != nil {
			return nil, err
		}
		if len(axes) == 0 {
			axes = append(axes, matrix.Axis{})
		}

		for _, axis := range axes {
			workflow := &model.Workflow{
				PipelineID: b.Curr.ID,
				PID:        pidSequence,
				State:      model.StatusPending,
				Environ:    axis,
				Name:       SanitizePath(y.Name),
			}

			workflowMetadata := frontend.MetadataFromStruct(b.Forge, b.Repo, b.Curr, b.Last, workflow, b.Link)
			environ := b.environmentVariables(workflowMetadata, axis)

			// add global environment variables for substituting
			for k, v := range b.Envs {
				if _, exists := environ[k]; exists {
					// don't override existing values
					continue
				}
				environ[k] = v
			}

			// substitute vars
			substituted, err := frontend.EnvVarSubst(string(y.Data), environ)
			if err != nil {
				return nil, err
			}

			// parse yaml pipeline
			parsed, err := yaml.ParseString(substituted)
			if err != nil {
				return nil, &yaml.PipelineParseError{Err: err}
			}

			// lint pipeline
			if err := linter.New(
				linter.WithTrusted(b.Repo.IsTrusted),
			).Lint(parsed); err != nil {
				return nil, &yaml.PipelineParseError{Err: err}
			}

			// checking if filtered.
			if match, err := parsed.When.Match(workflowMetadata, true); !match && err == nil {
				log.Debug().Str("pipeline", workflow.Name).Msg(
					"Marked as skipped, dose not match metadata",
				)
				workflow.State = model.StatusSkipped
			} else if err != nil {
				log.Debug().Str("pipeline", workflow.Name).Msg(
					"Pipeline config could not be parsed",
				)
				return nil, err
			}

			ir, err := b.toInternalRepresentation(parsed, environ, workflowMetadata, workflow.ID)
			if err != nil {
				return nil, err
			}

			if len(ir.Stages) == 0 {
				continue
			}

			item := &Item{
				Workflow:  workflow,
				Config:    ir,
				Labels:    parsed.Labels,
				DependsOn: parsed.DependsOn,
				RunsOn:    parsed.RunsOn,
				Platform:  parsed.Platform,
			}
			if item.Labels == nil {
				item.Labels = map[string]string{}
			}

			items = append(items, item)
			pidSequence++
		}
	}

	items = filterItemsWithMissingDependencies(items)

	// check if at least one step can start, if list is not empty
	if len(items) > 0 && !stepListContainsItemsToRun(items) {
		return nil, fmt.Errorf("pipeline has no startpoint")
	}

	return items, nil
}

func stepListContainsItemsToRun(items []*Item) bool {
	for i := range items {
		if items[i].Workflow.State == model.StatusPending {
			return true
		}
	}
	return false
}

func filterItemsWithMissingDependencies(items []*Item) []*Item {
	itemsToRemove := make([]*Item, 0)

	for _, item := range items {
		for _, dep := range item.DependsOn {
			if !containsItemWithName(dep, items) {
				itemsToRemove = append(itemsToRemove, item)
			}
		}
	}

	if len(itemsToRemove) > 0 {
		filtered := make([]*Item, 0)
		for _, item := range items {
			if !containsItemWithName(item.Workflow.Name, itemsToRemove) {
				filtered = append(filtered, item)
			}
		}
		// Recursive to handle transitive deps
		return filterItemsWithMissingDependencies(filtered)
	}

	return items
}

func containsItemWithName(name string, items []*Item) bool {
	for _, item := range items {
		if name == item.Workflow.Name {
			return true
		}
	}
	return false
}

func (b *StepBuilder) environmentVariables(metadata metadata.Metadata, axis matrix.Axis) map[string]string {
	environ := metadata.Environ()
	for k, v := range axis {
		environ[k] = v
	}
	return environ
}

func (b *StepBuilder) toInternalRepresentation(parsed *yaml_types.Workflow, environ map[string]string, metadata metadata.Metadata, stepID int64) (*backend_types.Config, error) {
	var secrets []compiler.Secret
	for _, sec := range b.Secs {
		if !sec.Match(b.Curr.Event) {
			continue
		}
		secrets = append(secrets, compiler.Secret{
			Name:       sec.Name,
			Value:      sec.Value,
			Match:      sec.Images,
			PluginOnly: sec.PluginsOnly,
		})
	}

	var registries []compiler.Registry
	for _, reg := range b.Regs {
		registries = append(registries, compiler.Registry{
			Hostname: reg.Address,
			Username: reg.Username,
			Password: reg.Password,
			Email:    reg.Email,
		})
	}

	return compiler.New(
		compiler.WithEnviron(environ),
		compiler.WithEnviron(b.Envs),
		// TODO: server deps should be moved into StepBuilder fields and set on StepBuilder creation
		compiler.WithEscalated(server.Config.Pipeline.Privileged...),
		compiler.WithResourceLimit(server.Config.Pipeline.Limits.MemSwapLimit, server.Config.Pipeline.Limits.MemLimit, server.Config.Pipeline.Limits.ShmSize, server.Config.Pipeline.Limits.CPUQuota, server.Config.Pipeline.Limits.CPUShares, server.Config.Pipeline.Limits.CPUSet),
		compiler.WithVolumes(server.Config.Pipeline.Volumes...),
		compiler.WithNetworks(server.Config.Pipeline.Networks...),
		compiler.WithLocal(false),
		compiler.WithOption(
			compiler.WithNetrc(
				b.Netrc.Login,
				b.Netrc.Password,
				b.Netrc.Machine,
			),
			b.Repo.IsSCMPrivate || server.Config.Pipeline.AuthenticatePublicRepos,
		),
		compiler.WithDefaultCloneImage(server.Config.Pipeline.DefaultCloneImage),
		compiler.WithRegistry(registries...),
		compiler.WithSecret(secrets...),
		compiler.WithPrefix(
			fmt.Sprintf(
				"wp_%s_%d",
				strings.ToLower(ulid.Make().String()),
				stepID,
			),
		),
		compiler.WithProxy(),
		compiler.WithWorkspaceFromURL("/woodpecker", b.Repo.Link),
		compiler.WithMetadata(metadata),
		compiler.WithTrusted(b.Repo.IsTrusted),
		compiler.WithNetrcOnlyTrusted(b.Repo.NetrcOnlyTrusted),
	).Compile(parsed)
}

// SetPipelineStepsOnPipeline is the link between pipeline representation in "pipeline package" and server
// to be specific this func currently is used to convert the pipeline.Item list (crafted by StepBuilder.Build()) into
// a pipeline that can be stored in the database by the server
func SetPipelineStepsOnPipeline(pipeline *model.Pipeline, pipelineItems []*Item) *model.Pipeline {
	var pidSequence int
	for _, item := range pipelineItems {
		if pidSequence < item.Workflow.PID {
			pidSequence = item.Workflow.PID
		}
	}

	for _, item := range pipelineItems {
		for _, stage := range item.Config.Stages {
			var gid int
			for _, step := range stage.Steps {
				pidSequence++
				if gid == 0 {
					gid = pidSequence
				}
				step := &model.Step{
					Name:       step.Alias,
					UUID:       step.UUID,
					PipelineID: pipeline.ID,
					PID:        pidSequence,
					PPID:       item.Workflow.PID,
					State:      model.StatusPending,
				}
				if item.Workflow.State == model.StatusSkipped {
					step.State = model.StatusSkipped
				}
				item.Workflow.Children = append(item.Workflow.Children, step)
			}
		}
		pipeline.Workflows = append(pipeline.Workflows, item.Workflow)
	}

	return pipeline
}

func SanitizePath(path string) string {
	path = filepath.Base(path)
	path = strings.TrimSuffix(path, ".yml")
	path = strings.TrimSuffix(path, ".yaml")
	path = strings.TrimPrefix(path, ".")
	return path
}
