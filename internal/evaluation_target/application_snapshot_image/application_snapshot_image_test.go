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

package application_snapshot_image

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	v02 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
	app "github.com/konflux-ci/application-api/api/v1alpha1"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"github.com/sigstore/cosign/v2/pkg/cosign"
	"github.com/sigstore/cosign/v2/pkg/oci"
	"github.com/sigstore/cosign/v2/pkg/oci/static"
	cosignTypes "github.com/sigstore/cosign/v2/pkg/types"
	"github.com/sigstore/sigstore/pkg/signature/payload"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/enterprise-contract/ec-cli/internal/attestation"
	"github.com/enterprise-contract/ec-cli/internal/policy"
	"github.com/enterprise-contract/ec-cli/internal/signature"
	"github.com/enterprise-contract/ec-cli/internal/utils"
	o "github.com/enterprise-contract/ec-cli/internal/utils/oci"
	"github.com/enterprise-contract/ec-cli/internal/utils/oci/fake"
)

// pipelineRunBuildType is the type of attestation we're interested in evaluating
const pipelineRunBuildType = "https://tekton.dev/attestations/chains/pipelinerun@v2"

type fakeAtt struct {
	statement  in_toto.ProvenanceStatementSLSA02
	signatures []signature.EntitySignature
}

func (f fakeAtt) Statement() []byte {
	bytes, err := json.Marshal(f.statement)
	if err != nil {
		panic(err)
	}
	return bytes
}

func (f fakeAtt) Type() string {
	return in_toto.StatementInTotoV01
}

func (f fakeAtt) PredicateType() string {
	return v02.PredicateSLSAProvenance
}

func (f fakeAtt) Signatures() []signature.EntitySignature {
	return f.signatures
}

func (f fakeAtt) Digest() map[string]string {
	return map[string]string{}
}

func (f fakeAtt) Subject() []in_toto.Subject {
	return []in_toto.Subject{}
}

type opts func(*fakeAtt)

func createSimpleAttestation(statement *in_toto.ProvenanceStatementSLSA02, o ...opts) attestation.Attestation {
	if statement == nil {
		statement = &in_toto.ProvenanceStatementSLSA02{
			StatementHeader: in_toto.StatementHeader{
				Type:          in_toto.StatementInTotoV01,
				PredicateType: v02.PredicateSLSAProvenance,
			},
			Predicate: v02.ProvenancePredicate{
				BuildType: pipelineRunBuildType,
			},
		}
	}

	a := fakeAtt{statement: *statement}

	for _, f := range o {
		f(&a)
	}

	return a
}

func TestWriteInputFile(t *testing.T) {
	cases := []struct {
		name     string
		snapshot ApplicationSnapshotImage
	}{
		{
			name: "single attestations",
			snapshot: ApplicationSnapshotImage{
				reference:    name.MustParseReference("registry.io/repository/image:tag"),
				attestations: []attestation.Attestation{createSimpleAttestation(nil)},
			},
		},
		{
			name: "multiple attestations",
			snapshot: ApplicationSnapshotImage{
				reference: name.MustParseReference("registry.io/repository/image:tag"),
				attestations: []attestation.Attestation{
					createSimpleAttestation(nil),
					createSimpleAttestation(nil),
				},
			},
		},
		{
			name: "image signatures",
			snapshot: ApplicationSnapshotImage{
				reference: name.MustParseReference("registry.io/repository/image:tag"),
				signatures: []signature.EntitySignature{
					{
						KeyID:     "keyId1",
						Signature: "signature1",
						Chain:     []string{"certificate1", "certificate2"},
					},
					{
						KeyID:     "keyId2",
						Signature: "signature2",
					},
				},
				attestations: []attestation.Attestation{
					createSimpleAttestation(nil),
				},
			},
		},
		{
			name: "image config",
			snapshot: ApplicationSnapshotImage{
				reference:  name.MustParseReference("registry.io/repository/image:tag"),
				configJSON: json.RawMessage(`{"Labels":{"io.k8s.display-name":"Test Image"}}`),
			},
		},
		{
			name: "parent image config",
			snapshot: ApplicationSnapshotImage{
				reference:        name.MustParseReference("registry.io/repository/image:tag"),
				parentConfigJSON: json.RawMessage(`{"Labels":{"io.k8s.display-name":"Base Image"}}`),
				parentRef:        name.MustParseReference("registry.io/repository/image/parent:tag"),
			},
		},
		{
			name: "attestation with signature",
			snapshot: ApplicationSnapshotImage{
				reference: name.MustParseReference("registry.io/repository/image:tag"),
				attestations: []attestation.Attestation{createSimpleAttestation(nil, func(a *fakeAtt) {
					a.signatures = append(a.signatures, signature.EntitySignature{
						KeyID:       "keyId",
						Signature:   "signature",
						Certificate: "certificate",
						Chain:       []string{"a", "b", "c"},
						Metadata: map[string]string{
							"k1": "v1",
							"k2": "v2",
						},
					})
				})},
			},
		},
		{
			name: "component with source",
			snapshot: ApplicationSnapshotImage{
				reference: name.MustParseReference("registry.io/repository/image:tag"),
				component: app.SnapshotComponent{
					ContainerImage: "registry.io/repository/image:tag",
					Source: app.ComponentSource{
						ComponentSourceUnion: app.ComponentSourceUnion{
							GitSource: &app.GitSource{
								URL:      "git.local/repository",
								Revision: "main",
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			ctx := utils.WithFS(context.Background(), fs)
			tt.snapshot.snapshot = app.SnapshotSpec{
				Components: []app.SnapshotComponent{
					{
						ContainerImage: "registry.io/repository/image:tag",
					},
					{
						ContainerImage: "registry.io/other-repository/image2:tag",
					},
				},
			}
			inputPath, inputJSON, err := tt.snapshot.WriteInputFile(ctx)

			assert.NoError(t, err)
			assert.NotEmpty(t, inputPath)
			assert.Regexp(t, `/ecp_input.\d+/input.json`, inputPath)
			fileExists, err := afero.Exists(fs, inputPath)
			assert.NoError(t, err)
			assert.True(t, fileExists)

			bytes, err := afero.ReadFile(fs, inputPath)
			assert.NoError(t, err)
			snaps.MatchJSON(t, bytes)

			assert.JSONEq(t, string(inputJSON), string(bytes))
		})
	}
}

func TestWriteInputFileMultipleAttestations(t *testing.T) {
	att := createSimpleAttestation(nil)
	snapshot := app.SnapshotSpec{
		Components: []app.SnapshotComponent{
			{
				ContainerImage: "registry.io/repository/image:tag",
			},
			{
				ContainerImage: "registry.io/other-repository/image2:tag",
			},
		},
	}
	a := ApplicationSnapshotImage{
		reference:    name.MustParseReference("registry.io/repository/image:tag"),
		attestations: []attestation.Attestation{att},
		snapshot:     snapshot,
	}

	fs := afero.NewMemMapFs()
	ctx := utils.WithFS(context.Background(), fs)
	inputPath, inputJSON, err := a.WriteInputFile(ctx)

	assert.NoError(t, err)
	assert.NotEmpty(t, inputPath)
	assert.Regexp(t, `/ecp_input.\d+/input.json`, inputPath)
	fileExists, err := afero.Exists(fs, inputPath)
	assert.NoError(t, err)
	assert.True(t, fileExists)

	bytes, err := afero.ReadFile(fs, inputPath)
	assert.NoError(t, err)
	snaps.MatchJSON(t, bytes)

	assert.JSONEq(t, string(inputJSON), string(bytes))
}

func TestNewApplicationSnapshotImage(t *testing.T) {
	ctx := context.Background()

	component := app.SnapshotComponent{
		ContainerImage: "registry.io/repository/image:tag",
	}
	policy, err := policy.NewOfflinePolicy(ctx, policy.Now)
	require.NoError(t, err)

	snapshot := app.SnapshotSpec{
		Components: []app.SnapshotComponent{
			{
				ContainerImage: "registry.io/repository/image:tag",
			},
			{
				ContainerImage: "registry.io/other-repository/image2:tag",
			},
		},
	}
	actual, err := NewApplicationSnapshotImage(ctx, component, policy, snapshot)
	assert.NoError(t, err)

	assert.Equal(t, len(actual.snapshot.Components), 2)
	assert.Equal(t, actual.component.ContainerImage, component.ContainerImage)
	assert.Equal(t, actual.snapshot.Components[0].ContainerImage, snapshot.Components[0].ContainerImage)
	assert.Equal(t, actual.snapshot.Components[1].ContainerImage, snapshot.Components[1].ContainerImage)
}

func TestSyntaxValidationWithoutAttestations(t *testing.T) {
	noAttestations := ApplicationSnapshotImage{}

	err := noAttestations.ValidateAttestationSyntax(context.TODO())
	assert.Error(t, err, "Expected error in validation")

	assert.True(t, strings.HasPrefix(err.Error(), "no attestation data"))
}

// Todo: Include some testing here for different attestation types.
// (I spent some time trying to find a nice way to make fakeAtt and
// createSimpleAttestation handle in_toto.Statement attestations as
// well as the original in_toto.ProvenanceStatementSLSA02 attestations
// but I wasn't able to figure it out.)
func TestSyntaxValidation(t *testing.T) {
	valid := createSimpleAttestation(&in_toto.ProvenanceStatementSLSA02{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV01,
			PredicateType: v02.PredicateSLSAProvenance,
			Subject: []in_toto.Subject{
				{
					Name: "hello",
					Digest: common.DigestSet{
						"sha1": "abcdef0123456789",
					},
				},
			},
		},
		Predicate: v02.ProvenancePredicate{
			BuildType: pipelineRunBuildType,
			Builder: common.ProvenanceBuilder{
				ID: "scheme:uri",
			},
		},
	})

	invalid := createSimpleAttestation(&in_toto.ProvenanceStatementSLSA02{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV01,
			PredicateType: v02.PredicateSLSAProvenance,
			Subject: []in_toto.Subject{
				{
					Name: "hello",
					Digest: common.DigestSet{
						"sha1": "abcdef0123456789",
					},
				},
			},
		},
		Predicate: v02.ProvenancePredicate{
			BuildType: pipelineRunBuildType,
			Builder: common.ProvenanceBuilder{
				ID: "invalid", // must be in URI syntax
			},
		},
	})

	cases := []struct {
		name         string
		attestations []attestation.Attestation
		err          *regexp.Regexp
	}{
		{
			name: "invalid",
			attestations: []attestation.Attestation{
				invalid,
			},
			err: regexp.MustCompile(`^attestation syntax validation failed: jsonschema: '/predicate/builder/id' does not validate with https://slsa.dev/provenance/v0.2#/properties/predicate/properties/builder/properties/id/format: 'invalid' is not valid 'uri'$`),
		},
		{
			name: "valid",
			attestations: []attestation.Attestation{
				valid,
			},
		},
		{
			name: "empty",
			attestations: []attestation.Attestation{
				createSimpleAttestation(&in_toto.ProvenanceStatementSLSA02{}),
			},
			err: regexp.MustCompile(`^attestation syntax validation failed: jsonschema: .*$`), // map order is not deterministic so each run produces a different error
		},
		{
			name: "valid and invalid",
			attestations: []attestation.Attestation{
				valid,
				invalid,
			},
			err: regexp.MustCompile(`^attestation syntax validation failed: jsonschema: '/predicate/builder/id' does not validate with https://slsa.dev/provenance/v0.2#/properties/predicate/properties/builder/properties/id/format: 'invalid' is not valid 'uri'$`),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := ApplicationSnapshotImage{
				attestations: c.attestations,
			}

			err := a.ValidateAttestationSyntax(context.TODO())
			if c.err == nil {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Regexp(t, c.err, err.Error())
			}
		})
	}
}

func TestValidateImageSignatureClaims(t *testing.T) {
	ref := name.MustParseReference("registry.io/repository/image:tag")
	a := ApplicationSnapshotImage{
		reference: ref,
	}

	c := fake.FakeClient{}

	ctx := o.WithClient(context.Background(), &c)

	c.On("VerifyImageSignatures", ref, mock.Anything).Return([]oci.Signature{}, false, nil)

	err := a.ValidateImageSignature(ctx)
	require.NoError(t, err)

	call := c.Calls[0]

	checkOpts := call.Arguments.Get(1).(*cosign.CheckOpts)
	assert.NotNil(t, checkOpts)

	claimVerifier := checkOpts.ClaimVerifier
	assert.NotNil(t, claimVerifier)

	cases := []struct {
		name        string
		payload     payload.SimpleContainerImage
		digest      v1.Hash
		annotations map[string]any
		err         error
	}{
		{
			name: "happy day",
			payload: payload.SimpleContainerImage{
				Critical: payload.Critical{
					Image: payload.Image{
						DockerManifestDigest: "sha256:dabbad00",
					},
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
		},
		{
			name: "happy day with annotations",
			payload: payload.SimpleContainerImage{
				Critical: payload.Critical{
					Image: payload.Image{
						DockerManifestDigest: "sha256:dabbad00",
					},
				},
				Optional: map[string]any{
					"a": "x",
					"b": "y",
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
			annotations: map[string]any{
				"a": "x",
				"b": "y",
			},
		},
		{
			name: "bad digest",
			payload: payload.SimpleContainerImage{
				Critical: payload.Critical{
					Image: payload.Image{
						DockerManifestDigest: "sha256:ffbaddD11",
					},
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
			err:    errors.New("invalid or missing digest in claim: sha256:ffbaddD11"),
		},
		{
			name: "missing annotation",
			payload: payload.SimpleContainerImage{
				Critical: payload.Critical{
					Image: payload.Image{
						DockerManifestDigest: "sha256:dabbad00",
					},
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
			annotations: map[string]any{
				"a": "x",
			},
			err: errors.New("missing or incorrect annotation"),
		},
		{
			name: "incorrect annotation",
			payload: payload.SimpleContainerImage{
				Critical: payload.Critical{
					Image: payload.Image{
						DockerManifestDigest: "sha256:dabbad00",
					},
				},
				Optional: map[string]any{
					"a": "y",
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
			annotations: map[string]any{
				"a": "x",
			},
			err: errors.New("missing or incorrect annotation"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload, err := json.Marshal(c.payload)
			require.NoError(t, err)

			signature, err := static.NewSignature(payload, "signature")
			require.NoError(t, err)

			err = claimVerifier(signature, c.digest, c.annotations)
			assert.Equal(t, c.err, err)
		})
	}
}

func TestValidateAttestationSignatureClaims(t *testing.T) {
	ref := name.MustParseReference("registry.io/repository/image:tag")
	a := ApplicationSnapshotImage{
		reference: ref,
	}

	c := fake.FakeClient{}

	ctx := o.WithClient(context.Background(), &c)

	c.On("VerifyImageAttestations", ref, mock.Anything).Return([]oci.Signature{}, false, nil)

	err := a.ValidateAttestationSignature(ctx)
	require.NoError(t, err)

	call := c.Calls[0]

	checkOpts := call.Arguments.Get(1).(*cosign.CheckOpts)
	assert.NotNil(t, checkOpts)

	claimVerifier := checkOpts.ClaimVerifier
	assert.NotNil(t, claimVerifier)

	cases := []struct {
		name      string
		statement in_toto.Statement
		digest    v1.Hash
		err       error
	}{
		{
			name: "happy day",
			statement: in_toto.Statement{
				StatementHeader: in_toto.StatementHeader{
					Subject: []in_toto.Subject{
						{
							Digest: map[string]string{
								"sha256": "dabbad00",
							},
						},
					},
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
		},
		{
			name: "happy day - multiple digests",
			statement: in_toto.Statement{
				StatementHeader: in_toto.StatementHeader{
					Subject: []in_toto.Subject{
						{
							Digest: map[string]string{
								"sha512": "dead10cc",
								"sha256": "dabbad00",
							},
						},
					},
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
		},
		{
			name: "no digests",
			statement: in_toto.Statement{
				StatementHeader: in_toto.StatementHeader{
					Subject: []in_toto.Subject{},
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
			err:    errors.New("no matching subject digest found"),
		},
		{
			name: "mismatched digests",
			statement: in_toto.Statement{
				StatementHeader: in_toto.StatementHeader{
					Subject: []in_toto.Subject{
						{
							Digest: map[string]string{
								"sha256": "dead10cc",
							},
						},
					},
				},
			},
			digest: v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
			err:    errors.New("no matching subject digest found"),
		},
		{
			name:      "empty statement",
			statement: in_toto.Statement{},
			digest:    v1.Hash{Algorithm: "sha256", Hex: "dabbad00"},
			err:       errors.New("no matching subject digest found"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			statementJSON, err := json.Marshal(c.statement)
			require.NoError(t, err)

			statement := base64.StdEncoding.EncodeToString(statementJSON)

			dsse := dsse.Envelope{
				Payload: statement,
			}

			payload, err := json.Marshal(dsse)
			require.NoError(t, err)

			signature, err := static.NewSignature(payload, "signature")
			require.NoError(t, err)

			err = claimVerifier(signature, c.digest, nil)
			assert.Equal(t, c.err, err)
		})
	}
}

func TestValidateImageSignatureWithCertificates(t *testing.T) {
	ref := name.MustParseReference("registry.io/repository/image:tag")
	a := ApplicationSnapshotImage{
		reference: ref,
	}

	c := fake.FakeClient{}

	ctx := o.WithClient(context.Background(), &c)

	sig, err := static.NewSignature(
		[]byte(`image`),
		"signature",
		static.WithLayerMediaType(types.MediaType((cosignTypes.DssePayloadType))),
		static.WithCertChain(
			signature.ChainguardReleaseCert,
			signature.SigstoreChainCert,
		),
	)
	require.NoError(t, err)

	c.On("VerifyImageSignatures", ref, mock.Anything).Return([]oci.Signature{sig}, false, nil)

	err = a.ValidateImageSignature(ctx)
	require.NoError(t, err)

	// split the chain into individual PEM certificates and restore the removed
	// separator chars
	chainAry := strings.Split(string(signature.SigstoreChainCert), "-\n-")
	for i, cer := range chainAry {
		switch {
		case i == 0:
			chainAry[i] = cer + "-\n"
		case i == len(chainAry)-1:
			chainAry[i] = "-" + cer
		default:
			chainAry[i] = "-" + cer + "\n"
		}
	}

	snaps.MatchSnapshot(t, a.signatures)
}

func TestFetchImageConfig(t *testing.T) {
	url := utils.WithDigest("registry.local/test-image")
	ctx := context.Background()
	ctx = fake.WithTestImageConfig(ctx, url)

	ref, err := name.ParseReference(url)
	require.NoError(t, err)
	a := ApplicationSnapshotImage{reference: ref}

	err = a.FetchImageConfig(ctx)
	require.NoError(t, err)

	require.Equal(t, string(a.configJSON), `{"Labels":{"io.k8s.display-name":"Test Image"}}`)
}

func TestFetchParentImageConfig(t *testing.T) {
	url := utils.WithDigest("registry.local/test-image")
	ctx := context.Background()
	ctx = fake.WithTestImageConfig(ctx, url)

	ref, err := name.ParseReference(url)
	require.NoError(t, err)
	a := ApplicationSnapshotImage{reference: ref}

	err = a.FetchParentImageConfig(ctx)
	require.NoError(t, err)

	require.Equal(t, string(a.parentConfigJSON), `{"Labels":{"io.k8s.display-name":"Base Image"}}`)
}

func TestFetchImageFiles(t *testing.T) {
	ref := name.MustParseReference("registry.io/repository/image:tag")
	a := ApplicationSnapshotImage{reference: ref}

	image, err := crane.Image(map[string][]byte{
		"manifests/csv.yaml": []byte(
			`apiVersion: operators.coreos.com/v1alpha1
kind: ClusterServiceVersion`),
	})
	require.NoError(t, err)
	image, err = mutate.Config(image, v1.Config{
		Labels: map[string]string{
			"operators.operatorframework.io.bundle.manifests.v1": "manifests/",
		},
	})
	require.NoError(t, err)

	client := fake.FakeClient{}
	client.On("Image", ref, mock.Anything).Return(image, nil)

	ctx := o.WithClient(context.Background(), &client)

	err = a.FetchImageFiles(ctx)
	require.NoError(t, err)

	require.Equal(t, map[string]json.RawMessage{
		"manifests/csv.yaml": json.RawMessage(`{"apiVersion":"operators.coreos.com/v1alpha1","kind":"ClusterServiceVersion"}`),
	}, a.files)
}
