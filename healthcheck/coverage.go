package main

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// skipDirs are directories we never descend into when looking for coverage
// output — dependency trees and build artifacts, not source.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
}

type coverageReport struct {
	Path      string  `json:"path"`                 // relative to the repo root, e.g. "packages/api/coverage"
	Percent   float64 `json:"percent"`              // line coverage percentage; -1 if a report dir was found but couldn't be parsed
	Source    string  `json:"source"`                // report file the percentage was derived from
	ReportURL string  `json:"report_url,omitempty"` // served via /files/, empty if no HTML report was found
}

// findCoverageReports walks repoPath looking for directories named "coverage"
// (monorepos may have several, one per package) and tries to extract a line
// coverage percentage from whatever report format is inside.
func findCoverageReports(repoPath string) []coverageReport {
	var reports []coverageReport

	filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != repoPath && skipDirs[name] {
			return filepath.SkipDir
		}
		if name != "coverage" {
			return nil
		}

		rel, err := filepath.Rel(repoPath, path)
		if err != nil {
			rel = path
		}
		reports = append(reports, parseCoverageDir(path, rel))
		return filepath.SkipDir
	})

	return reports
}

func parseCoverageDir(dirPath, relPath string) coverageReport {
	reportURL := reportURLFor(dirPath)

	if pct, ok := parseIstanbulSummary(filepath.Join(dirPath, "coverage-summary.json")); ok {
		return coverageReport{Path: relPath, Percent: pct, Source: "coverage-summary.json", ReportURL: reportURL}
	}
	if pct, ok := parseLcov(filepath.Join(dirPath, "lcov.info")); ok {
		return coverageReport{Path: relPath, Percent: pct, Source: "lcov.info", ReportURL: reportURL}
	}
	return coverageReport{Path: relPath, Percent: -1, Source: "unknown", ReportURL: reportURL}
}

// reportURLFor looks for a generated HTML report inside a coverage directory
// (Jest/nyc's lcov reporter nests it under lcov-report/, others write it
// directly) and, if found, returns the URL it's served at via /files/.
func reportURLFor(dirPath string) string {
	candidates := []string{
		filepath.Join(dirPath, "lcov-report", "index.html"),
		filepath.Join(dirPath, "index.html"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			rel, err := filepath.Rel(workspaceDir, c)
			if err != nil {
				return ""
			}
			return "/files/" + filepath.ToSlash(rel)
		}
	}
	return ""
}

func parseIstanbulSummary(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}

	var summary struct {
		Total struct {
			Lines struct {
				Pct float64 `json:"pct"`
			} `json:"lines"`
		} `json:"total"`
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		return 0, false
	}
	return summary.Total.Lines.Pct, true
}

// parseLcov sums LF (lines found) and LH (lines hit) across every record in
// an lcov.info file to compute an aggregate line-coverage percentage.
func parseLcov(path string) (float64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	var found, hit int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "LF:"):
			found += parseInt64(line[len("LF:"):])
		case strings.HasPrefix(line, "LH:"):
			hit += parseInt64(line[len("LH:"):])
		}
	}
	if found == 0 {
		return 0, false
	}
	return float64(hit) / float64(found) * 100, true
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
