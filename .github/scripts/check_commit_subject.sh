#!/usr/bin/env bash

# Copyright (c) 2026, WSO2 LLC. (http://www.wso2.com).
#
# WSO2 LLC. licenses this file to you under the Apache License,
# Version 2.0 (the "License"); you may not use this file except
# in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Checks one commit subject against Conventional Commits: check_commit_subject.sh "<subject>".
# The PR title is checked by the Lint PR workflow with the same types.

set -euo pipefail

types='feat|fix|build|chore|ci|docs|style|refactor|perf|test|revert'
max_length=72

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <commit subject>" >&2
    exit 2
fi
subject="$1"
errors=()
if [[ ! "$subject" =~ ^($types)(\([^\)]+\))?!?:\ [a-z] ]]; then
    errors+=("does not match <type>(<optional scope>): <description>, with a lowercase description")
fi
if (( ${#subject} > max_length )); then
    errors+=("is ${#subject} characters, more than the $max_length allowed")
fi
if [[ "$subject" == *. ]]; then
    errors+=("ends with a period")
fi
if (( ${#errors[@]} == 0 )); then
    exit 0
fi
{
    echo "Invalid commit subject \"$subject\":"
    printf -- '- %s\n' "${errors[@]}"
    echo
    echo "Allowed types: ${types//|/, }. The description starts with a lowercase verb in the imperative mood,"
    echo "has no final period, and the subject is at most $max_length characters."
} >&2
exit 1
