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

package covercheck

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const profile = `mode: set
example.com/m/internal/a/a.go:1.1,3.2 4 1
example.com/m/internal/a/a.go:4.1,6.2 4 0
example.com/m/cmd/tool/main.go:1.1,3.2 10 0
`

func TestCoverageCountsStatementsOutsideCmd(t *testing.T) {
	covered, total, err := Coverage(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if covered != 4 || total != 8 {
		t.Fatalf("coverage = %d/%d, want 4/8", covered, total)
	}
}

func TestCoverageMergesBlocksAcrossProfiles(t *testing.T) {
	second := "mode: set\nexample.com/m/internal/a/a.go:4.1,6.2 4 1\n"
	covered, total, err := Coverage(strings.NewReader(profile), strings.NewReader(second))
	if err != nil {
		t.Fatal(err)
	}
	if covered != 8 || total != 8 {
		t.Fatalf("coverage = %d/%d, want 8/8: a block covered in any profile counts once", covered, total)
	}
}

func TestCoverageRejectsMalformedLines(t *testing.T) {
	for _, line := range []string{"a.go:1.1,2.2 4", "a.go:1.1,2.2 x 1", "a.go:1.1,2.2 4 y"} {
		if _, _, err := Coverage(strings.NewReader(line)); err == nil {
			t.Errorf("Coverage(%q) accepted a malformed line", line)
		}
	}
}

func writeProfile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cover.out")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckPassesAtOrAboveMinimum(t *testing.T) {
	var out bytes.Buffer
	if err := Check(&out, 50, []string{writeProfile(t, profile)}); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "Coverage: 50.0% of 8 statements (minimum 50%)") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCheckFailsBelowMinimum(t *testing.T) {
	err := Check(&bytes.Buffer{}, 75, []string{writeProfile(t, profile)})
	if err == nil || !strings.Contains(err.Error(), "50.0% is below the required 75%") {
		t.Fatalf("Check = %v, want the below-minimum error", err)
	}
}

func TestCheckFailsWithoutStatements(t *testing.T) {
	err := Check(&bytes.Buffer{}, 75, []string{writeProfile(t, "mode: set\nexample.com/m/cmd/x/main.go:1.1,2.2 3 1\n")})
	if err == nil || !strings.Contains(err.Error(), "no statements found") {
		t.Fatalf("Check = %v, want the no-statements error", err)
	}
}

func TestCheckFailsWithoutProfilesOrMissingFile(t *testing.T) {
	if err := Check(&bytes.Buffer{}, 75, nil); err == nil {
		t.Fatal("Check accepted an empty profile list")
	}
	if err := Check(&bytes.Buffer{}, 75, []string{filepath.Join(t.TempDir(), "absent.out")}); err == nil {
		t.Fatal("Check accepted a missing profile")
	}
}
