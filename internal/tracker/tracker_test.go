// Copyright The Conforma Contributors
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
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package tracker

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	hd "github.com/MakeNowJust/heredoc"
	gba "github.com/Maldris/go-billy-afero"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/spf13/afero"
	"github.com/spf13/afero/zipfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pipeline "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/tektoncd/pipeline/pkg/remote/oci"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/enterprise-contract/ec-cli/internal/image"
	"github.com/enterprise-contract/ec-cli/internal/utils"
)

var sampleHashOne = v1.Hash{
	Algorithm: "sha256",
	Hex:       "01ba4719c80b6fe911b091a7c05124b64eeece964e09c058ef8f9805daca546b",
}

var sampleHashOneUpdated = v1.Hash{
	Algorithm: "sha256",
	Hex:       "8b4683472773680a36b58ba96d321aa82c4f59248d5614348deca12adce61f39",
}

var sampleHashTwo = v1.Hash{
	Algorithm: "sha256",
	Hex:       "a2c1615816029636903a8172775682e8bbb84c6fde8d74b6de1e198f19f95c72",
}

var sampleHashThree = v1.Hash{
	Algorithm: "sha256",
	Hex:       "284e3029cce3ae5ee0b05866100e300046359f53ae4c77fe6b34c05aa7a72cee",
}

var (
	expectedInEffectDays    = 30
	expectedEffectiveOnTime = time.Now().Add(time.Duration(expectedInEffectDays) * oneDay).UTC().Round(oneDay)
	expectedEffectiveOn     = expectedEffectiveOnTime.Format(time.RFC3339)
	expectedExpiresOn       = expectedEffectiveOn
)

var (
	todayUTC    = time.Now().UTC()
	yesterday   = todayUTC.Add(time.Hour * 24 * -1).Format(time.RFC3339)
	tomorrow    = todayUTC.Add(time.Hour * 24).Format(time.RFC3339)
	inOneDay    = tomorrow
	inTwoDays   = todayUTC.Add(time.Hour * 24 * 2).Format(time.RFC3339)
	inThreeDays = todayUTC.Add(time.Hour * 24 * 3).Format(time.RFC3339)
	inFourDays  = todayUTC.Add(time.Hour * 24 * 4).Format(time.RFC3339)
)

func TestTrack(t *testing.T) {
	tests := []struct {
		name    string
		urls    []string
		prune   bool
		output  string
		input   []byte
		freshen bool
	}{
		{
			name: "always insert at the front",
			urls: []string{
				"registry.com/repo:two@" + sampleHashTwo.String(),
				"registry.com/repo:one@" + sampleHashOne.String(),
			},
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/repo:one:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
				  oci://registry.com/repo:two:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashTwo.String() + `
			`),
		},
		{
			name: "multiple repos",
			urls: []string{
				"registry.com/one:1.0@" + sampleHashOne.String(),
				"registry.com/two:2.0@" + sampleHashTwo.String(),
			},
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/one:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
				  oci://registry.com/two:2.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashTwo.String() + `
			`),
		},
		{
			name: "update existing repo",
			urls: []string{
				"registry.com/repo:two@" + sampleHashTwo.String(),
			},
			input: []byte(hd.Doc(
				`---
				trusted_tasks:
				  oci://registry.com/repo:one:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
			`)),
			output: hd.Doc(
				`---
				trusted_tasks:
				  oci://registry.com/repo:one:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
				  oci://registry.com/repo:two:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashTwo.String() + `
			`),
		},
		{
			name: "update existing collection",
			urls: []string{
				"registry.com/two:2.0@" + sampleHashTwo.String(),
			},
			input: []byte(hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/one:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
			`)),
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/one:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
				  oci://registry.com/two:2.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashTwo.String() + `
			`),
		},
		{
			name: "create new collection",
			urls: []string{
				"registry.com/two:2.0@" + sampleHashTwo.String(),
			},
			input: []byte(hd.Doc(`
				---
			`)),
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/two:2.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashTwo.String() + `
			`),
		},
		{
			name: "mixed tasks and pipelines",
			urls: []string{
				"registry.com/mixed:1.0@" + sampleHashOne.String(),
			},
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
			`),
		},
		{
			name: "prefer older entries with same digest",
			urls: []string{
				"registry.com/one:1.0@" + sampleHashOne.String(),
			},
			input: []byte(hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/one:1.0:
				    - effective_on: "` + tomorrow + `"
				      ref: ` + sampleHashOne.String() + `
			`)),
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/one:1.0:
				    - effective_on: "` + tomorrow + `"
				      ref: ` + sampleHashOne.String() + `
			`),
		},
		{
			name: "prune older entries",
			urls: []string{
				"registry.com/mixed:1.0@" + sampleHashOne.String(),
			},
			prune: true,
			input: []byte(hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:1.0:
				    - effective_on: "` + yesterday + `"
				      ref: ` + sampleHashThree.String() + `
				    - effective_on: "` + yesterday + `"
				      ref: ` + sampleHashTwo.String() + `
			`)),
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
				    - effective_on: "` + yesterday + `"
				      expires_on: "` + expectedExpiresOn + `"
				      ref: ` + sampleHashThree.String() + `
			`),
		},
		{
			name: "prune entries with same digest if adjacent",
			urls: []string{
				"registry.com/mixed:1.0@" + sampleHashOne.String(),
			},
			prune: true,
			input: []byte(hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:0.2:
				    - effective_on: "` + inTwoDays + `"
				      ref: ` + sampleHashTwo.String() + `
				    - effective_on: "` + inOneDay + `"
				      ref: ` + sampleHashTwo.String() + `
				  oci://registry.com/mixed:0.3:
				    - effective_on: "` + inTwoDays + `"
				      ref: ` + sampleHashThree.String() + `
				    - effective_on: "` + inOneDay + `"
				      ref: ` + sampleHashThree.String() + `
			`)),
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:0.2:
				    - effective_on: "` + inOneDay + `"
				      ref: ` + sampleHashTwo.String() + `
				  oci://registry.com/mixed:0.3:
				    - effective_on: "` + inOneDay + `"
				      ref: ` + sampleHashThree.String() + `
				  oci://registry.com/mixed:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
			`),
		},
		{
			name: "don't prune entries with same digest if not adjacent",
			urls: []string{
				"registry.com/mixed:1.0@" + sampleHashOne.String(),
			},
			prune: true,
			input: []byte(hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:1.0:
				    - effective_on: "` + inFourDays + `"
				      ref: ` + sampleHashThree.String() + `
				    - effective_on: "` + inThreeDays + `"
				      ref: ` + sampleHashTwo.String() + `
				    - effective_on: "` + inTwoDays + `"
				      ref: ` + sampleHashThree.String() + `
				    - effective_on: "` + inOneDay + `"
				      ref: ` + sampleHashTwo.String() + `
			`)),
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
				    - effective_on: "` + inFourDays + `"
				      expires_on: "` + expectedExpiresOn + `"
				      ref: ` + sampleHashThree.String() + `
				    - effective_on: "` + inThreeDays + `"
				      expires_on: "` + inFourDays + `"
				      ref: ` + sampleHashTwo.String() + `
				    - effective_on: "` + inTwoDays + `"
				      expires_on: "` + inThreeDays + `"
				      ref: ` + sampleHashThree.String() + `
				    - effective_on: "` + inOneDay + `"
				      expires_on: "` + inTwoDays + `"
				      ref: ` + sampleHashTwo.String() + `
			`),
		},
		{
			name:    "freshen existing input",
			freshen: true,
			input: []byte(hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/one:1.0:
				    - effective_on: "` + tomorrow + `"
				      ref: ` + sampleHashOne.String() + `
			`)),
			output: hd.Doc(`
			---
			trusted_tasks:
			  oci://registry.com/one:1.0:
			    - effective_on: "` + expectedEffectiveOn + `"
			      ref: ` + sampleHashOneUpdated.String() + `
			    - effective_on: "` + tomorrow + `"
			      expires_on: "` + expectedExpiresOn + `"
			      ref: ` + sampleHashOne.String() + `
			`),
		},
		{
			name: "trusted_tasks takes precedence over task-bundles",
			urls: []string{
				"registry.com/mixed:1.0@" + sampleHashOne.String(),
			},
			prune: true,
			input: []byte(hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
			`)),
			output: hd.Doc(`
				---
				trusted_tasks:
				  oci://registry.com/mixed:1.0:
				    - effective_on: "` + expectedEffectiveOn + `"
				      ref: ` + sampleHashOne.String() + `
			`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), image.RemoteHead, head)

			client := fakeClient{objects: testObjects, images: testImages}
			ctx = WithClient(ctx, client)

			output, err := Track(ctx, tt.urls, tt.input, tt.prune, tt.freshen, expectedInEffectDays)
			require.NoError(t, err)
			require.Equal(t, tt.output, string(output))
		})
	}
}

type fakeClient struct {
	objects map[string]map[string]map[string]runtime.Object
	images  map[string]v1.Image
}

func (r fakeClient) GetTektonObject(ctx context.Context, bundle, kind, name string) (runtime.Object, error) {
	if bundle, ok := r.objects[bundle]; ok {
		if names, ok := bundle[kind]; ok {
			if obj, ok := names[name]; ok {
				return obj, nil
			}
		}
	}
	return nil, fmt.Errorf("resource named %q of kind %q not found", name, kind)
}

func (r fakeClient) GetImage(ctx context.Context, ref name.Reference) (v1.Image, error) {
	if image, ok := r.images[ref.String()]; ok {
		return image, nil
	}
	return nil, fmt.Errorf("image %q not found", ref)
}

func head(ref name.Reference, options ...remote.Option) (*v1.Descriptor, error) {
	if d, ok := testTags[ref]; ok {
		return d, nil
	} else {
		return nil, fmt.Errorf("%q not found", ref)
	}
}

var testObjects = map[string]map[string]map[string]runtime.Object{
	"registry.com/one:1.0@" + sampleHashOne.String(): {
		"pipeline": {
			"pipeline-v1": mustCreateFakePipelineObject(),
		},
	},
	"registry.com/two:2.0@" + sampleHashTwo.String(): {
		"pipeline": {
			"pipeline-v2": mustCreateFakePipelineObject(),
		},
	},
	"registry.com/repo:one@" + sampleHashOne.String(): {
		"pipeline": {
			"pipeline-v1": mustCreateFakePipelineObject(),
		},
	},
	"registry.com/repo:two@" + sampleHashTwo.String(): {
		"pipeline": {
			"pipeline-v2": mustCreateFakePipelineObject(),
		},
	},
	"registry.com/mixed:1.0@" + sampleHashOne.String(): {
		"pipeline": {
			"pipeline-v1": mustCreateFakePipelineObject(),
		},
	},
}

var testImages = map[string]v1.Image{
	"registry.com/one:1.0@" + sampleHashOne.String(): mustCreateFakeBundleImage([]fakeDefinition{
		{name: "task-v1", kind: "task"},
	}),
	"registry.com/one:1.0@" + sampleHashOneUpdated.String(): mustCreateFakeBundleImage([]fakeDefinition{
		{name: "task-v1", kind: "task"},
	}),
	"registry.com/two:2.0@" + sampleHashTwo.String(): mustCreateFakeBundleImage([]fakeDefinition{
		{name: "task-v2", kind: "task"},
	}),
	"registry.com/repo:one@" + sampleHashOne.String(): mustCreateFakeBundleImage([]fakeDefinition{
		{name: "task-v1", kind: "task"},
	}),
	"registry.com/repo:two@" + sampleHashTwo.String(): mustCreateFakeBundleImage([]fakeDefinition{
		{name: "task-v2", kind: "task"},
	}),
	"registry.com/mixed:1.0@" + sampleHashOne.String(): mustCreateFakeBundleImage([]fakeDefinition{
		{name: "pipeline-v1", kind: "pipeline"},
		{name: "task-v1", kind: "task"},
	}),
}

var testTags = map[name.Reference]*v1.Descriptor{
	name.MustParseReference("registry.com/one:1.0"): {
		Digest: sampleHashOneUpdated,
	},
}

type fakeDefinition struct {
	name string
	kind string
}

func mustCreateFakeBundleImage(defs []fakeDefinition) v1.Image {
	adds := make([]mutate.Addendum, 0, len(defs))

	for _, definition := range defs {
		l, err := random.Layer(0, types.DockerLayer)
		if err != nil {
			panic("unable to create layer for test data")
		}
		adds = append(adds, mutate.Addendum{
			Layer: l,
			Annotations: map[string]string{
				oci.KindAnnotation:  definition.kind,
				oci.TitleAnnotation: definition.name,
			},
		})
	}

	img, err := mutate.Append(empty.Image, adds...)
	if err != nil {
		panic(err)
	}
	return img
}

func mustCreateFakePipelineObject() runtime.Object {
	gitCloneTask := pipeline.PipelineTask{
		TaskRef: &pipeline.TaskRef{
			Name: "git-clone",
		},
	}
	buildahTask := pipeline.PipelineTask{
		TaskRef: &pipeline.TaskRef{
			ResolverRef: pipeline.ResolverRef{
				Resolver: "bundle",
				Params: []pipeline.Param{
					{
						Name: "name",
						Value: pipeline.ParamValue{
							StringVal: "buildah",
						},
					},
				},
			},
		},
	}
	summaryTask := pipeline.PipelineTask{
		TaskRef: &pipeline.TaskRef{
			Name: "summary",
		},
	}
	p := pipeline.Pipeline{}
	p.SetDefaults(context.Background())
	p.Spec.Tasks = []pipeline.PipelineTask{gitCloneTask, buildahTask}
	p.Spec.Finally = []pipeline.PipelineTask{summaryTask}

	return &p
}

func TestGroupUrls(t *testing.T) {
	urls := []string{"registry.io/repository/image:tag", "git+https://git.io/organization/repository", "rhcr.io/repository/image:tag", "git+ssh://got.io/organization/repository"}

	imgs, gits := groupUrls(urls)

	assert.Equal(t, imgs, []string{"registry.io/repository/image:tag", "rhcr.io/repository/image:tag"})
	assert.Equal(t, gits, []string{"git+https://git.io/organization/repository", "git+ssh://got.io/organization/repository"})
}

func TestTrackGitReferences(t *testing.T) {
	tracker := &Tracker{
		TrustedTasks: make(map[string][]taskRecord),
	}

	require.NoError(t, tracker.trackGitReferences(context.Background(), []string{
		"git+https://git.io/organization/repository//task1.yaml@rev1",
		"git+ssh://got.io/organization/repository//dir/task2.yaml@rev2",
	}, false, expectedEffectiveOnTime))

	expected := map[string][]taskRecord{
		"git+https://git.io/organization/repository//task1.yaml": {{
			Ref:         "rev1",
			Repository:  "git+https://git.io/organization/repository//task1.yaml",
			EffectiveOn: expectedEffectiveOnTime,
		}},
		"git+ssh://got.io/organization/repository//dir/task2.yaml": {{
			Ref:         "rev2",
			Repository:  "git+ssh://got.io/organization/repository//dir/task2.yaml",
			EffectiveOn: expectedEffectiveOnTime,
		}},
	}

	if !cmp.Equal(tracker.TrustedTasks, expected) {
		t.Errorf("expected vs got: %s", cmp.Diff(tracker.TrustedTasks, expected))
	}
}

func TestTrackGitReferencesWithoutCommitId(t *testing.T) {
	tracker := &Tracker{
		TrustedTasks: make(map[string][]taskRecord),
	}

	fs := afero.NewMemMapFs()
	ctx := utils.WithFS(context.Background(), fs)

	f, err := os.Open("testdata/repository.zip")
	require.NoError(t, err)

	i, err := os.Stat("testdata/repository.zip")
	require.NoError(t, err)

	z, err := zip.NewReader(f, i.Size())
	require.NoError(t, err)

	rfs := gba.New(zipfs.New(z), "", false)

	client.InstallProtocol("test", server.NewServer(server.NewFilesystemLoader(rfs)))

	require.NoError(t, tracker.trackGitReferences(ctx, []string{
		"git+test://git.io/repository/.git//tasks/task1/0.1/task.yaml",
		"git+test://git.io/repository/.git//tasks/task2/0.2/task.yaml",
	}, true, expectedEffectiveOnTime))

	expected := map[string][]taskRecord{
		"git+test://git.io/repository/.git//tasks/task1/0.1/task.yaml": {{
			Ref:         "0916963bac30ea708c0ded4dd9d160fc148fd46f",
			Repository:  "git+test://git.io/repository/.git//tasks/task1/0.1/task.yaml",
			EffectiveOn: expectedEffectiveOnTime,
		}},
		"git+test://git.io/repository/.git//tasks/task2/0.2/task.yaml": {{
			Ref:         "acf3f1907b51c0e15809a61536bba71809daec68",
			Repository:  "git+test://git.io/repository/.git//tasks/task2/0.2/task.yaml",
			EffectiveOn: expectedEffectiveOnTime,
		}},
	}

	if !cmp.Equal(tracker.TrustedTasks, expected) {
		t.Errorf("expected vs got: %s", cmp.Diff(tracker.TrustedTasks, expected))
	}

	// check to make sure we do not leave temp files around
	matches, err := afero.Glob(fs, "tmp/*")
	require.NoError(t, err)
	assert.Nil(t, matches)
}

func TestTrackGitReferencesWithoutFreshen(t *testing.T) {
	tracker := &Tracker{
		TrustedTasks: map[string][]taskRecord{
			"git+test://git.io/repository/.git//tasks/task1/0.1/task.yaml": {{
				Ref:         "f0cacc1a",
				Repository:  "git+test://git.io/repository/.git//tasks/task1/0.1/task.yaml",
				EffectiveOn: expectedEffectiveOnTime,
			}},
		},
	}

	fs := afero.NewMemMapFs()
	ctx := utils.WithFS(context.Background(), fs)

	f, err := os.Open("testdata/repository.zip")
	require.NoError(t, err)

	i, err := os.Stat("testdata/repository.zip")
	require.NoError(t, err)

	z, err := zip.NewReader(f, i.Size())
	require.NoError(t, err)

	rfs := gba.New(zipfs.New(z), "", false)

	client.InstallProtocol("test", server.NewServer(server.NewFilesystemLoader(rfs)))

	require.NoError(t, tracker.trackGitReferences(ctx, []string{
		"git+test://git.io/repository/.git//tasks/task2/0.2/task.yaml",
	}, true, expectedEffectiveOnTime))

	expected := map[string][]taskRecord{
		"git+test://git.io/repository/.git//tasks/task1/0.1/task.yaml": {{
			Ref:         "0916963bac30ea708c0ded4dd9d160fc148fd46f",
			Repository:  "git+test://git.io/repository/.git//tasks/task1/0.1/task.yaml",
			EffectiveOn: expectedEffectiveOnTime,
		}, {
			Ref:         "f0cacc1a",
			Repository:  "git+test://git.io/repository/.git//tasks/task1/0.1/task.yaml",
			EffectiveOn: expectedEffectiveOnTime,
		}},
		"git+test://git.io/repository/.git//tasks/task2/0.2/task.yaml": {{
			Ref:         "acf3f1907b51c0e15809a61536bba71809daec68",
			Repository:  "git+test://git.io/repository/.git//tasks/task2/0.2/task.yaml",
			EffectiveOn: expectedEffectiveOnTime,
		}},
	}

	if !cmp.Equal(tracker.TrustedTasks, expected) {
		t.Errorf("expected vs got: %s", cmp.Diff(tracker.TrustedTasks, expected))
	}

	// check to make sure we do not leave temp files around
	matches, err := afero.Glob(fs, "tmp/*")
	require.NoError(t, err)
	assert.Nil(t, matches)
}

func TestInEffectDays(t *testing.T) {
	ctx := context.WithValue(context.Background(), image.RemoteHead, head)

	client := fakeClient{objects: testObjects, images: testImages}
	ctx = WithClient(ctx, client)

	inEffectDays := 666
	expectedEffectiveOn := time.Now().Add(time.Duration(inEffectDays) * oneDay).UTC().Round(oneDay).Format(time.RFC3339)

	urls := []string{
		"registry.com/mixed:1.0@" + sampleHashOne.String(),
	}

	expected := hd.Doc(`
		---
		trusted_tasks:
		  oci://registry.com/mixed:1.0:
		    - effective_on: "` + expectedEffectiveOn + `"
		      ref: ` + sampleHashOne.String() + `
	`)

	output, err := Track(ctx, urls, nil, true, false, inEffectDays)
	require.NoError(t, err)
	require.Equal(t, expected, string(output))
}
