package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseInt64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"42", 42},
		{"  7 ", 7},
		{"not-a-number", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseInt64(c.in); got != c.want {
			t.Errorf("parseInt64(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseIstanbulSummary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coverage-summary.json")
	content := `{"total":{"lines":{"pct":87.5}}}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pct, ok := parseIstanbulSummary(path)
	if !ok || pct != 87.5 {
		t.Errorf("parseIstanbulSummary() = (%v, %v), want (87.5, true)", pct, ok)
	}

	if _, ok := parseIstanbulSummary(filepath.Join(dir, "missing.json")); ok {
		t.Error("expected ok=false for missing file")
	}

	badPath := filepath.Join(dir, "bad.json")
	os.WriteFile(badPath, []byte("not json"), 0644)
	if _, ok := parseIstanbulSummary(badPath); ok {
		t.Error("expected ok=false for malformed JSON")
	}
}

func TestParseLcov(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lcov.info")
	content := "SF:a.js\nLF:10\nLH:8\nend_of_record\nSF:b.js\nLF:10\nLH:2\nend_of_record\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pct, ok := parseLcov(path)
	if !ok || pct != 50 {
		t.Errorf("parseLcov() = (%v, %v), want (50, true)", pct, ok)
	}

	if _, ok := parseLcov(filepath.Join(dir, "missing.info")); ok {
		t.Error("expected ok=false for missing file")
	}

	zeroPath := filepath.Join(dir, "zero.info")
	os.WriteFile(zeroPath, []byte("SF:a.js\nend_of_record\n"), 0644)
	if _, ok := parseLcov(zeroPath); ok {
		t.Error("expected ok=false when LF sums to zero")
	}
}

func TestParseCoverageDir(t *testing.T) {
	withWorkspaceDir(t, t.TempDir())

	istanbulDir := filepath.Join(workspaceDir, "istanbul")
	os.MkdirAll(istanbulDir, 0755)
	os.WriteFile(filepath.Join(istanbulDir, "coverage-summary.json"), []byte(`{"total":{"lines":{"pct":90}}}`), 0644)

	r := parseCoverageDir(istanbulDir, "istanbul")
	if r.Percent != 90 || r.Source != "coverage-summary.json" {
		t.Errorf("parseCoverageDir(istanbul) = %+v", r)
	}

	lcovDir := filepath.Join(workspaceDir, "lcov")
	os.MkdirAll(lcovDir, 0755)
	os.WriteFile(filepath.Join(lcovDir, "lcov.info"), []byte("LF:4\nLH:4\n"), 0644)

	r = parseCoverageDir(lcovDir, "lcov")
	if r.Percent != 100 || r.Source != "lcov.info" {
		t.Errorf("parseCoverageDir(lcov) = %+v", r)
	}

	emptyDir := filepath.Join(workspaceDir, "empty")
	os.MkdirAll(emptyDir, 0755)

	r = parseCoverageDir(emptyDir, "empty")
	if r.Percent != -1 || r.Source != "unknown" {
		t.Errorf("parseCoverageDir(empty) = %+v, want Percent=-1 Source=unknown", r)
	}
}

func TestFindCoverageReports(t *testing.T) {
	withWorkspaceDir(t, t.TempDir())
	repoDir := filepath.Join(workspaceDir, "repo")

	topCoverage := filepath.Join(repoDir, "coverage")
	os.MkdirAll(topCoverage, 0755)
	os.WriteFile(filepath.Join(topCoverage, "coverage-summary.json"), []byte(`{"total":{"lines":{"pct":50}}}`), 0644)

	nestedCoverage := filepath.Join(repoDir, "pkg", "coverage")
	os.MkdirAll(nestedCoverage, 0755)
	os.WriteFile(filepath.Join(nestedCoverage, "coverage-summary.json"), []byte(`{"total":{"lines":{"pct":75}}}`), 0644)

	excludedCoverage := filepath.Join(repoDir, "node_modules", "somedep", "coverage")
	os.MkdirAll(excludedCoverage, 0755)
	os.WriteFile(filepath.Join(excludedCoverage, "coverage-summary.json"), []byte(`{"total":{"lines":{"pct":1}}}`), 0644)

	reports := findCoverageReports(repoDir)
	if len(reports) != 2 {
		t.Fatalf("findCoverageReports() returned %d reports, want 2: %+v", len(reports), reports)
	}

	byPath := map[string]coverageReport{}
	for _, r := range reports {
		byPath[filepath.ToSlash(r.Path)] = r
	}
	if r, ok := byPath["coverage"]; !ok || r.Percent != 50 {
		t.Errorf("top-level coverage report = %+v, ok=%v", r, ok)
	}
	if r, ok := byPath["pkg/coverage"]; !ok || r.Percent != 75 {
		t.Errorf("nested coverage report = %+v, ok=%v", r, ok)
	}
}

func TestReportURLFor(t *testing.T) {
	withWorkspaceDir(t, t.TempDir())

	lcovReportDir := filepath.Join(workspaceDir, "repo", "coverage")
	os.MkdirAll(filepath.Join(lcovReportDir, "lcov-report"), 0755)
	os.WriteFile(filepath.Join(lcovReportDir, "lcov-report", "index.html"), []byte("<html></html>"), 0644)
	if got, want := reportURLFor(lcovReportDir), "/files/repo/coverage/lcov-report/index.html"; got != want {
		t.Errorf("reportURLFor(lcov-report case) = %q, want %q", got, want)
	}

	plainDir := filepath.Join(workspaceDir, "repo2", "coverage")
	os.MkdirAll(plainDir, 0755)
	os.WriteFile(filepath.Join(plainDir, "index.html"), []byte("<html></html>"), 0644)
	if got, want := reportURLFor(plainDir), "/files/repo2/coverage/index.html"; got != want {
		t.Errorf("reportURLFor(plain index.html case) = %q, want %q", got, want)
	}

	neitherDir := filepath.Join(workspaceDir, "repo3", "coverage")
	os.MkdirAll(neitherDir, 0755)
	if got := reportURLFor(neitherDir); got != "" {
		t.Errorf("reportURLFor(no report) = %q, want empty string", got)
	}
}
