#!/usr/bin/env bash
# Copyright The Conforma Contributors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# SPDX-License-Identifier: Apache-2.0

# Updates a local clone of redhat-appstudio/build-definitions to use the latest
# Task bundle produced by this repository.
# Usage:
#   update-build-definitions.sh <PATH_TO_BUILD_DEFINITIONS> [<TAG>]

set -o errexit
set -o pipefail
set -o nounset

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

TARGET_DIR="${1}"
cd "${TARGET_DIR}" || exit 1

echo 'Resolving task bundle...'
TASK_BUNDLE_TAG="${2:-snapshot}"
MANIFEST=$(mktemp --tmpdir)
function cleanup() {
    rm "${MANIFEST}"
}
trap cleanup EXIT
skopeo inspect "docker://quay.io/enterprise-contract/ec-task-bundle:${TASK_BUNDLE_TAG}" --raw > "${MANIFEST}"
TASK_BUNDLE_DIGEST="$(skopeo manifest-digest "${MANIFEST}")"
REVISION="$(jq -r '.annotations["org.opencontainers.image.revision"]' "${MANIFEST}")"
if [[ -z "${KEEP_TAG:-}" && -n "${REVISION}" && "${REVISION}" != null ]]; then
    TASK_BUNDLE_TAG="${REVISION}"
fi
# Sanity check
diff \
    <(skopeo inspect --raw "docker://quay.io/enterprise-contract/ec-task-bundle:${TASK_BUNDLE_TAG}") \
    <(skopeo inspect --raw "docker://quay.io/enterprise-contract/ec-task-bundle@${TASK_BUNDLE_DIGEST}")
TASK_BUNDLE_REF="quay.io/enterprise-contract/ec-task-bundle:${TASK_BUNDLE_TAG}@${TASK_BUNDLE_DIGEST}"
echo "Resolved bundle is ${TASK_BUNDLE_REF}"

function update() {
    local definition
    definition="${1}"
    echo "# Processing ${definition}"
    echo -n "Double-checking parameters of the pipeline are contained within the task parameters... "
    jq --exit-status \
        --slurpfile pipeline <(yq -o json '.spec.tasks[] | select(.name == "verify") | [.params[].name]' "${definition}") \
        'contains($pipeline[0])' \
        <(cd "${ROOT}"; go run -modfile tools/go.mod github.com/tektoncd/cli/cmd/tkn bundle list --output json "${TASK_BUNDLE_REF}" task verify-enterprise-contract 2>/dev/null| jq '.spec.params | map(.name)')

    echo 'Updating build-deployments...'
    REF="${TASK_BUNDLE_REF}" yq e -i \
        '.spec.tasks[] |= select(.name == "verify").taskRef.params[] |= select(.name == "bundle").value |= env(REF)' \
        "${definition}"
    echo
}

for f in pipelines/enterprise-contract*.yaml; do
    update "${f}"
done

echo 'build-definitions updated successfully'
