package workspace

import (
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

type ignoreMatcher interface {
	Match(relativePath string, isDirectory bool) bool
	WithRules(rules ignoreRules) ignoreMatcher
}

type ignoreRules []gitignore.Pattern

type gitIgnoreMatcher struct {
	patterns []gitignore.Pattern
	matcher  gitignore.Matcher
}

func newIgnoreMatcher() ignoreMatcher {
	return gitIgnoreMatcher{matcher: gitignore.NewMatcher(nil)}
}

func (matcher gitIgnoreMatcher) Match(relativePath string, isDirectory bool) bool {
	return matcher.matcher.Match(strings.Split(relativePath, "/"), isDirectory)
}

func parseIgnoreRules(
	relativeDirectory string,
	data []byte,
) ignoreRules {
	domain := splitPath(relativeDirectory)
	added := make([]gitignore.Pattern, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "#") || len(strings.TrimSpace(line)) == 0 {
			continue
		}
		added = append(added, gitignore.ParsePattern(line, domain))
	}
	return added
}

func (matcher gitIgnoreMatcher) WithRules(rules ignoreRules) ignoreMatcher {
	patterns := make([]gitignore.Pattern, 0, len(matcher.patterns)+len(rules))
	patterns = append(patterns, matcher.patterns...)
	patterns = append(patterns, rules...)
	return gitIgnoreMatcher{
		patterns: patterns,
		matcher:  gitignore.NewMatcher(patterns),
	}
}

func splitPath(relativePath string) []string {
	if relativePath == "" {
		return nil
	}
	return strings.Split(relativePath, "/")
}
