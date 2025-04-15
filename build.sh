#!/usr/bin/bash
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

set -o errexit
set -o nounset
set -o pipefail

BUILDS="${1}"

# Generally blank, but will be set to "redhat" for Konflux builds
BUILD_SUFFIX="${2:-""}"

EC_FULL_VERSION=$(hack/derive-version.sh "${BUILD_SUFFIX}")

echo "EC_FULL_VERSION=$EC_FULL_VERSION"
echo "BUILDS=$BUILDS"

export GO_COMPLIANCE_INFO=0

build_ec() {
    GOOS="$1" GOARCH="$2" go build \
        -trimpath \
        --mod=readonly \
        -ldflags="-s -w -X github.com/enterprise-contract/ec-cli/internal/version.Version=$4" \
        -o "dist/$3"
    sha256sum -b "dist/$3" > "dist/$3.sha256"
}

build_kubectl() {
    GOOS="$1" GOARCH="$2" go install \
        -modfile tools/kubectl/go.mod \
        -trimpath \
        --mod=readonly \
        k8s.io/kubernetes/cmd/kubectl

    gobin="$(go env GOBIN)"
    gopath="$(go env GOPATH)"
    binpath="${gobin:-${gopath:-$HOME/go}/bin}"
    kubectlbin="${binpath}/$1_$2/$3"
    if [[ "$1" == "$(go env GOOS)" && $2 == "$(go env GOARCH)" ]]; then
      kubectlbin="${binpath}/$3"
    fi

    cp "${kubectlbin}" "dist/$3_$1_$2"
}

for os_arch in ${BUILDS}; do
    GOOS="${os_arch%_*}"
    GOARCH="${os_arch#*_}"
    [[ "$GOOS" == "windows" ]] && DOT_EXE=".exe" || DOT_EXE=""
    BINFILE="ec_${GOOS}_${GOARCH}${DOT_EXE}"
    echo "Building ${BINFILE} for ${EC_FULL_VERSION}"
    build_ec "${GOOS}" "${GOARCH}" "${BINFILE}" "${EC_FULL_VERSION}"

    KUBECTLBIN="kubectl${DOT_EXE}"
    echo "Building ${GOOS}/${GOARCH} of ${KUBECTLBIN} version $(go list -modfile tools/go.mod -mod=readonly -f '{{.Version}}' -m k8s.io/kubernetes)"
    build_kubectl "${GOOS}" "${GOARCH}" "${KUBECTLBIN}"
done
