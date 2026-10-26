// Copyright (c) 2026, WSO2 LLC. (http://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// Command covercheck fails when the Go coverage profiles are below a minimum: covercheck -min 75 build/coverage/*.out
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wso2/integration-workflow-runtime/tools/internal/covercheck"
)

func main() {
	minimum := flag.Float64("min", 75, "required statement coverage, in percent")
	flag.Parse()
	if err := covercheck.Check(os.Stdout, *minimum, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
