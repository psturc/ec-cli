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

# The Conforma golden container, see https://github.com/enterprise-contract/golden-container/
IMAGE=${IMAGE:-"ghcr.io/enterprise-contract/golden-container:latest"}
IDENTITY_REGEXP=${IDENTITY_REGEXP:-"https:\/\/github\.com\/(slsa-framework\/slsa-github-generator|enterprise-contract\/golden-container)\/"}
IDENTITY_ISSUER=${IDENTITY_ISSUER:-"https://token.actions.githubusercontent.com"}

# Festoji, see https://github.com/lcarva/festoji
#IMAGE=${IMAGE:-"quay.io/lucarval/festoji:latest"}
#IDENTITY_REGEXP=${IDENTITY_REGEXP:-"https:\/\/github\.com\/(slsa-framework\/slsa-github-generator|lcarva\/festoji)\/"}
#IDENTITY_ISSUER=${IDENTITY_ISSUER:-"https://token.actions.githubusercontent.com"}

POLICY_YAML=${POLICY_YAML:-"github.com/enterprise-contract/config//github-default"}
#POLICY_YAML=${POLICY_YAML:-"./policy.yaml"}

OUTPUT=${OUTPUT:-yaml}

MAIN_GO=$(git rev-parse --show-toplevel)/main.go

# Use `EC=ec` to avoid recompiling
EC=${EC:-"go run $MAIN_GO"}

$EC validate image \
  --image "${IMAGE}" \
  --policy "${POLICY_YAML}" \
  --certificate-identity-regexp ${IDENTITY_REGEXP} \
  --certificate-oidc-issuer ${IDENTITY_ISSUER} \
  --show-successes \
  --info \
  --output ${OUTPUT} \
  "$@"
