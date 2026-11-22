// Package conformance checks the repository files OpenTofu cannot enforce: every repository of an owner is
// licensed under Apache-2.0 and carries no community health files of its own, so the defaults from the
// owner's .github repository apply.
package conformance

import (
	"cmp"
	"slices"
	"strings"
)

const (
	RequiredLicence = "Apache-2.0"

	// DefaultsRepository is the repository GitHub takes default community health files from.
	DefaultsRepository = ".github"
)

const (
	RuleLicence   Rule = "licence"
	RuleOverrides Rule = "community health override"
)

var (
	overrideFiles = []string{
		"CODE_OF_CONDUCT",
		"CONTRIBUTING",
		"SECURITY",
		"SUPPORT",
		"ACCESSIBILITY",
		"FUNDING",
		"VULNERABILITY_REPORT",
		"pull_request_template",
	}
	overrideDirectories = []string{
		"ISSUE_TEMPLATE",
		"DISCUSSION_TEMPLATE",
		"PULL_REQUEST_TEMPLATE",
	}
)

type Rule string

type Entry struct {
	Name  string
	IsDir bool
}

// Repository is what the rules read of a repository's default branch. Licence is empty when GitHub detects none.
type Repository struct {
	Owner     string
	Name      string
	Licence   string
	Root      []Entry
	DotGitHub []Entry
	Docs      []Entry
	Fork      bool
	Archived  bool
	Empty     bool
}

type Violation struct {
	Repository string
	Rule       Rule
	Detail     string
}

type Result struct {
	Violations []Violation
	Checked    int
}

func (r *Repository) Skipped() bool {
	return r.Fork || r.Archived || r.Empty
}

func (v *Violation) String() string {
	return v.Repository + ": " + string(v.Rule) + ": " + v.Detail
}

func Check(repositories []Repository) Result {
	var result Result
	for i := range repositories {
		repo := &repositories[i]
		if repo.Skipped() {
			continue
		}
		result.Checked++
		fullName := repo.Owner + "/" + repo.Name
		if detail, ok := licenceViolation(repo.Licence); !ok {
			result.Violations = append(result.Violations, Violation{Repository: fullName, Rule: RuleLicence, Detail: detail})
		}
		if repo.Name == DefaultsRepository {
			continue
		}
		if paths := overridePaths(repo); len(paths) > 0 {
			result.Violations = append(result.Violations,
				Violation{Repository: fullName, Rule: RuleOverrides, Detail: strings.Join(paths, ", ")})
		}
	}
	slices.SortStableFunc(result.Violations, func(a, b Violation) int {
		return cmp.Compare(a.Repository, b.Repository)
	})
	return result
}

func licenceViolation(licence string) (string, bool) {
	switch licence {
	case RequiredLicence:
		return "", true
	case "":
		return "none detected, " + RequiredLicence + " required", false
	default:
		return licence + " detected, " + RequiredLicence + " required", false
	}
}

func overridePaths(repo *Repository) []string {
	var paths []string
	paths = appendOverrides(paths, "", repo.Root)
	paths = appendOverrides(paths, ".github/", repo.DotGitHub)
	return appendOverrides(paths, "docs/", repo.Docs)
}

func appendOverrides(paths []string, prefix string, entries []Entry) []string {
	for _, entry := range entries {
		if isOverride(entry) {
			path := prefix + entry.Name
			if entry.IsDir {
				path += "/"
			}
			paths = append(paths, path)
		}
	}
	return paths
}

func isOverride(entry Entry) bool {
	if entry.IsDir {
		return containsFold(overrideDirectories, entry.Name)
	}
	stem, _, _ := strings.Cut(entry.Name, ".")
	return containsFold(overrideFiles, stem)
}

func containsFold(names []string, name string) bool {
	return slices.ContainsFunc(names, func(candidate string) bool {
		return strings.EqualFold(candidate, name)
	})
}
