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

// Package covercheck fails when Go statement coverage, outside the thin cmd/ wrappers, is below a minimum.
package covercheck

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// excludedSegments marks packages whose statements do not count: cmd/ wrappers are covered by the Ballerina suites.
var excludedSegments = []string{"/cmd/"}

type block struct {
	statements int
	covered    bool
}

// Coverage returns the covered and total statements in the profiles, counting each block once across profiles.
func Coverage(profiles ...io.Reader) (covered, total int, err error) {
	blocks := map[string]block{}
	for _, profile := range profiles {
		scanner := bufio.NewScanner(profile)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "mode:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) != 3 {
				return 0, 0, fmt.Errorf("malformed profile line %q", line)
			}
			if excluded(fields[0]) {
				continue
			}
			statements, err := strconv.Atoi(fields[1])
			if err != nil {
				return 0, 0, fmt.Errorf("malformed profile line %q: %w", line, err)
			}
			count, err := strconv.Atoi(fields[2])
			if err != nil {
				return 0, 0, fmt.Errorf("malformed profile line %q: %w", line, err)
			}
			previous := blocks[fields[0]]
			blocks[fields[0]] = block{statements: statements, covered: count > 0 || previous.covered}
		}
		if err := scanner.Err(); err != nil {
			return 0, 0, err
		}
	}
	for _, b := range blocks {
		total += b.statements
		if b.covered {
			covered += b.statements
		}
	}
	return covered, total, nil
}

func excluded(location string) bool {
	for _, segment := range excludedSegments {
		if strings.Contains(location, segment) {
			return true
		}
	}
	return false
}

// Check reads the profile files, prints the coverage and returns an error below the minimum percentage.
func Check(out io.Writer, minimum float64, paths []string) error {
	if len(paths) == 0 {
		return errors.New("no coverage profiles given")
	}
	readers := make([]io.Reader, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		readers = append(readers, file)
	}
	covered, total, err := Coverage(readers...)
	if err != nil {
		return err
	}
	if total == 0 {
		return errors.New("no statements found in the coverage profiles")
	}
	percent := 100 * float64(covered) / float64(total)
	_, _ = fmt.Fprintf(out, "Coverage: %.1f%% of %d statements (minimum %.0f%%)\n", percent, total, minimum)
	if percent < minimum {
		return fmt.Errorf("coverage %.1f%% is below the required %.0f%%", percent, minimum)
	}
	return nil
}
