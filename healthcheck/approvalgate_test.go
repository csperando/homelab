package main

import "testing"

func TestIsGatedCommand(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"git commit -m 'wip'", true},
		{"git commit --amend", true},
		{"git push", true},
		{"git push origin main", true},
		{"git push --force origin main", true},
		{"rm -rf node_modules", true},
		{"rm -fr /tmp/scratch", true},
		{"cd /root/workspace/repo && git push origin main", true},
		{"echo hi; git commit -am 'x'", true},

		{"git status", false},
		{"git diff", false},
		{"git add .", false},
		{"git log --oneline", false},
		{"ls -la", false},
		{"cat README.md", false},
		{"npm test", false},
		{"rm file.txt", false},
		{"rm -r somedir", false},
		{"", false},
	}

	for _, c := range cases {
		if got := isGatedCommand(c.command); got != c.want {
			t.Errorf("isGatedCommand(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}
