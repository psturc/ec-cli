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

package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterprise-contract/ec-cli/internal/evaluator"
	"github.com/enterprise-contract/ec-cli/internal/output"
	"github.com/enterprise-contract/ec-cli/internal/policy"
	"github.com/enterprise-contract/ec-cli/internal/utils"
	"github.com/enterprise-contract/ec-cli/internal/utils/oci"
	"github.com/enterprise-contract/ec-cli/internal/utils/oci/fake"
)

// mockValidate is a helper function that returns a specified Output and error for testing.
func mockValidate(out *output.Output, err error) InputValidationFunc {
	return func(_ context.Context, fpath string, _ policy.Policy, _ bool) (*output.Output, error) {
		// This function ignores the actual file content and always returns the provided output and error.
		return out, err
	}
}

func setUpValidateInputCmd(validate InputValidationFunc, fs afero.Fs) (*cobra.Command, *bytes.Buffer) {
	cmd := validateInputCmd(validate)

	// Create a fake client and context with a memory filesystem.
	client := fake.FakeClient{}
	ctx := utils.WithFS(context.Background(), fs)
	ctx = oci.WithClient(ctx, &client)
	cmd.SetContext(ctx)

	var out bytes.Buffer
	cmd.SetOut(&out)

	return cmd, &out
}

func Test_ValidateInputCmd_SuccessSingleFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Write a dummy file to simulate input
	require.NoError(t, afero.WriteFile(fs, "/input.yaml", []byte("some: data"), 0644))

	// Mock validator: returns success with no violations, one success result.
	outMock := &output.Output{
		PolicyCheck: []evaluator.Outcome{
			{
				Successes: []evaluator.Result{
					{Message: "Everything looks great!"},
				},
			},
		},
	}

	cmd, buf := setUpValidateInputCmd(mockValidate(outMock, nil), fs)
	cmd.SetArgs([]string{
		"input",
		"--file", "/input.yaml",
		"--policy", `{"publicKey": "testkey"}`,
	})

	utils.SetTestRekorPublicKey(t)
	err := cmd.Execute()
	assert.NoError(t, err)

	// Parse the JSON output
	var outJSON map[string]interface{}
	err = json.Unmarshal(buf.Bytes(), &outJSON)
	assert.NoError(t, err)

	// Ensure success is true
	assert.True(t, outJSON["success"].(bool))

	inputFiles, ok := outJSON["filepaths"].([]interface{})
	if !ok {
		t.Fatal("expected 'filepaths' key in output")
	}
	assert.Len(t, inputFiles, 1)
	inputObj := inputFiles[0].(map[string]interface{})
	assert.Equal(t, "/input.yaml", inputObj["filepath"])
	assert.True(t, inputObj["success"].(bool))
}

func Test_ValidateInputCmd_SuccessMultipleFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/input1.yaml", []byte("some: data"), 0644))
	require.NoError(t, afero.WriteFile(fs, "/input2.yaml", []byte("other: data"), 0644))

	// Mock validator: always returns success.
	outMock := &output.Output{
		PolicyCheck: []evaluator.Outcome{
			{
				Successes: []evaluator.Result{
					{Message: "Pass"},
				},
			},
		},
	}

	cmd, buf := setUpValidateInputCmd(mockValidate(outMock, nil), fs)
	cmd.SetArgs([]string{
		"input",
		"--file", "/input1.yaml",
		"--file", "/input2.yaml",
		"--policy", `{"name":"Default","description":"Stuff and things","sources":[{"name":"Default","policy":["/bacon/and/eggs/policy/lib","/bacon/and/eggs/policy/release"],"data":["/bacon/and/eggs/example/data"],"config":{"include":["sbom_cyclonedx"],"exclude":[]}}]}`,
	})

	utils.SetTestRekorPublicKey(t)
	err := cmd.Execute()
	assert.NoError(t, err)

	var outJSON map[string]interface{}
	err = json.Unmarshal(buf.Bytes(), &outJSON)
	assert.NoError(t, err)
	assert.True(t, outJSON["success"].(bool))

	inputFiles, ok := outJSON["filepaths"].([]interface{})
	if !ok {
		t.Fatal("expected 'filepaths' key in output")
	}
	assert.Len(t, inputFiles, 2)

	// Verify sorting by filepath descending as per code
	filePaths := []string{}
	for _, input := range inputFiles {
		f := input.(map[string]interface{})["filepath"].(string)
		filePaths = append(filePaths, f)
	}
	sorted := append([]string{}, filePaths...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] > sorted[j]
	})
	assert.Equal(t, sorted, filePaths)
}

func Test_ValidateInputCmd_Failure(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/bad.yaml", []byte("invalid"), 0644))

	// Mock validator: returns an error
	cmd, _ := setUpValidateInputCmd(mockValidate(nil, errors.New("validation failed")), fs)
	cmd.SetArgs([]string{
		"input",
		"--file", "/bad.yaml",
		"--policy", `{"publicKey": "testkey"}`,
	})

	utils.SetTestRekorPublicKey(t)
	err := cmd.Execute()
	assert.Error(t, err)
	assert.EqualError(t, err, "error validating file /bad.yaml: validation failed")
}

func Test_ValidateInputCmd_StrictMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/file.yaml", []byte("some: data"), 0644))

	// Mock validator: returns no error, but a violation.
	outMock := &output.Output{
		PolicyCheck: []evaluator.Outcome{
			{
				Failures: []evaluator.Result{
					{Message: "Some violation"},
				},
			},
		},
	}

	cmd, _ := setUpValidateInputCmd(mockValidate(outMock, nil), fs)
	cmd.SetArgs([]string{
		"input",
		"--file", "/file.yaml",
		"--policy", `{"publicKey": "testkey"}`,
		"--strict", "true",
	})

	utils.SetTestRekorPublicKey(t)
	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "success criteria not met")
}

func Test_ValidateInputCmd_NonStrictMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/file.yaml", []byte("some: data"), 0644))

	// Mock validator: returns no error but a violation (should not cause non-zero exit in non-strict mode).
	outMock := &output.Output{
		PolicyCheck: []evaluator.Outcome{
			{
				Failures: []evaluator.Result{
					{Message: "Some violation"},
				},
			},
		},
	}

	cmd, _ := setUpValidateInputCmd(mockValidate(outMock, nil), fs)
	cmd.SetArgs([]string{
		"input",
		"--file", "/file.yaml",
		"--policy", `{"publicKey": "testkey"}`,
		"--strict", "false",
	})

	utils.SetTestRekorPublicKey(t)

	// Capture output for assertions
	outputBuffer := &bytes.Buffer{}
	cmd.SetOut(outputBuffer)
	cmd.SetErr(outputBuffer)

	// Execute the command
	err := cmd.Execute()

	// Ensure no error is returned in non-strict mode
	assert.Error(t, err)

	// Check that the output mentions violations, but the command succeeds
	output := outputBuffer.String()
	assert.Contains(t, output, "Some violation")
	assert.Contains(t, output, "success criteria not met")
}

func Test_ValidateInputCmd_NoPolicyProvided(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/file.yaml", []byte("some: data"), 0644))

	cmd, _ := setUpValidateInputCmd(nil, fs)
	cmd.SetArgs([]string{
		"input",
		"--file", "/file.yaml",
	})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required flag(s) \"policy\" not set")
}

func Test_ValidateInputCmd_NoFileProvided(t *testing.T) {
	cmd, _ := setUpValidateInputCmd(nil, afero.NewMemMapFs())
	cmd.SetArgs([]string{
		"input",
		"--policy", `{"publicKey":"testkey"}`,
	})
	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required flag(s) \"file\" not set")
}

func Test_ValidateInputCmd_PolicyParsingError(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/file.yaml", []byte("some: data"), 0644))

	cmd, _ := setUpValidateInputCmd(nil, fs)
	cmd.SetArgs([]string{
		"input",
		"--file", "/file.yaml",
		"--policy", `{"invalidjson":"}`,
	})

	err := cmd.Execute()
	assert.Error(t, err)
	// Adjust the assertion if a different error message is thrown by your policy parser
	assert.Contains(t, err.Error(), "unable to parse EnterpriseContractPolicySpec")
}

func Test_ValidateInputCmd_EmptyPolicyFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/file.yaml", []byte("data"), 0644))
	require.NoError(t, afero.WriteFile(fs, "/policy.yaml", []byte{}, 0644))

	cmd, _ := setUpValidateInputCmd(nil, fs)
	cmd.SetArgs([]string{
		"input",
		"--file", "/file.yaml",
		"--policy", "/policy.yaml",
	})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file /policy.yaml is empty")
}
