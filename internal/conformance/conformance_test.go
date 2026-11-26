//go:build unit

package conformance_test

import (
	"slices"
	"testing"

	"github.com/abrosimov/gh-repo-settings/internal/conformance"
)

const testOwner = "abrosimov"

func apacheRepository(name string) conformance.Repository {
	return conformance.Repository{Owner: testOwner, Name: name, Licence: conformance.RequiredLicence}
}

func file(name string) conformance.Entry { return conformance.Entry{Name: name} }

func directory(name string) conformance.Entry { return conformance.Entry{Name: name, IsDir: true} }

func equalViolations(got, want []conformance.Violation) bool {
	return slices.Equal(got, want)
}

func TestCheckOverrideMatching(t *testing.T) {
	locations := []struct {
		name   string
		prefix string
		place  func(repo *conformance.Repository, entry conformance.Entry)
	}{
		{"root", "", func(repo *conformance.Repository, entry conformance.Entry) { repo.Root = []conformance.Entry{entry} }},
		{".github", ".github/", func(repo *conformance.Repository, entry conformance.Entry) {
			repo.DotGitHub = []conformance.Entry{entry}
		}},
		{"docs", "docs/", func(repo *conformance.Repository, entry conformance.Entry) { repo.Docs = []conformance.Entry{entry} }},
	}
	cases := []struct {
		name    string
		entry   conformance.Entry
		flagged string
	}{
		{"CONTRIBUTING without extension", file("CONTRIBUTING"), "CONTRIBUTING"},
		{"lower-case contributing.md", file("contributing.md"), "contributing.md"},
		{"mixed-case Security.RST", file("Security.RST"), "Security.RST"},
		{"double extension", file("CODE_OF_CONDUCT.en.md"), "CODE_OF_CONDUCT.en.md"},
		{"lower-case pull_request_template", file("pull_request_template.md"), "pull_request_template.md"},
		{"upper-case PULL_REQUEST_TEMPLATE", file("PULL_REQUEST_TEMPLATE.md"), "PULL_REQUEST_TEMPLATE.md"},
		{"SUPPORT", file("SUPPORT.md"), "SUPPORT.md"},
		{"ACCESSIBILITY", file("ACCESSIBILITY.md"), "ACCESSIBILITY.md"},
		{"FUNDING", file("FUNDING.yml"), "FUNDING.yml"},
		{"VULNERABILITY_REPORT", file("VULNERABILITY_REPORT.md"), "VULNERABILITY_REPORT.md"},
		{"ISSUE_TEMPLATE directory", directory("ISSUE_TEMPLATE"), "ISSUE_TEMPLATE/"},
		{"lower-case issue_template directory", directory("issue_template"), "issue_template/"},
		{"DISCUSSION_TEMPLATE directory", directory("DISCUSSION_TEMPLATE"), "DISCUSSION_TEMPLATE/"},
		{"PULL_REQUEST_TEMPLATE directory", directory("PULL_REQUEST_TEMPLATE"), "PULL_REQUEST_TEMPLATE/"},
		{"mixed-case Pull_Request_Template directory", directory("Pull_Request_Template"), "Pull_Request_Template/"},

		{"CONTRIBUTORS is another file", file("CONTRIBUTORS.md"), ""},
		{"prefixed MY_CONTRIBUTING", file("MY_CONTRIBUTING.md"), ""},
		{"suffixed CONTRIBUTING-guide", file("CONTRIBUTING-guide.md"), ""},
		{"hidden .contributing", file(".contributing"), ""},
		{"README", file("README.md"), ""},
		{"LICENSE", file("LICENSE"), ""},
		{"directory named CONTRIBUTING", directory("CONTRIBUTING"), ""},
		{"directory named SECURITY.md", directory("SECURITY.md"), ""},
		{"file named ISSUE_TEMPLATE", file("ISSUE_TEMPLATE"), ""},
		{"file named DISCUSSION_TEMPLATE", file("DISCUSSION_TEMPLATE"), ""},
		{"directory src", directory("src"), ""},
		{"directory Docs in the root", directory("Docs"), ""},
	}
	for _, location := range locations {
		for _, tc := range cases {
			t.Run(location.name+"/"+tc.name, func(t *testing.T) {
				repo := apacheRepository("project")
				location.place(&repo, tc.entry)

				result := conformance.Check([]conformance.Repository{repo})

				if result.Checked != 1 {
					t.Errorf("Checked = %d, want 1", result.Checked)
				}
				var want []conformance.Violation
				if tc.flagged != "" {
					want = []conformance.Violation{{
						Repository: "abrosimov/project",
						Rule:       conformance.RuleOverrides,
						Detail:     location.prefix + tc.flagged,
					}}
				}
				if !equalViolations(result.Violations, want) {
					t.Errorf("Violations = %+v, want %+v", result.Violations, want)
				}
			})
		}
	}
}

func TestCheckJoinsSeveralOverridesOfOneRepositoryIntoOneViolation(t *testing.T) {
	repo := apacheRepository("project")
	repo.Docs = []conformance.Entry{file("README.md"), file("SECURITY.md")}
	repo.DotGitHub = []conformance.Entry{directory("ISSUE_TEMPLATE"), file("workflows"), file("FUNDING.yml")}
	repo.Root = []conformance.Entry{file("main.go"), file("SUPPORT.md"), file("CODE_OF_CONDUCT.md")}

	result := conformance.Check([]conformance.Repository{repo})

	want := []conformance.Violation{{
		Repository: "abrosimov/project",
		Rule:       conformance.RuleOverrides,
		Detail:     "SUPPORT.md, CODE_OF_CONDUCT.md, .github/ISSUE_TEMPLATE/, .github/FUNDING.yml, docs/SECURITY.md",
	}}
	if !equalViolations(result.Violations, want) {
		t.Errorf("Violations = %+v, want %+v", result.Violations, want)
	}
}

func TestCheckExemptsTheDefaultsRepositoryFromOverridesOnly(t *testing.T) {
	overrides := func(repo conformance.Repository) conformance.Repository {
		repo.Root = []conformance.Entry{file("CODE_OF_CONDUCT.md"), file("README.md")}
		repo.DotGitHub = []conformance.Entry{directory("ISSUE_TEMPLATE")}
		repo.Docs = []conformance.Entry{file("SECURITY.md")}
		return repo
	}

	t.Run("Apache-2.0 defaults repository has no violation", func(t *testing.T) {
		result := conformance.Check([]conformance.Repository{overrides(apacheRepository(conformance.DefaultsRepository))})

		if len(result.Violations) != 0 || result.Checked != 1 {
			t.Errorf("result = %+v, want no violations and one repository checked", result)
		}
	})

	t.Run("other licence still violates the licence rule", func(t *testing.T) {
		repo := overrides(apacheRepository(conformance.DefaultsRepository))
		repo.Licence = "MIT"

		result := conformance.Check([]conformance.Repository{repo})

		want := []conformance.Violation{{
			Repository: "abrosimov/.github",
			Rule:       conformance.RuleLicence,
			Detail:     "MIT detected, Apache-2.0 required",
		}}
		if !equalViolations(result.Violations, want) {
			t.Errorf("Violations = %+v, want %+v", result.Violations, want)
		}
	})

	t.Run("exemption does not extend to a repository that merely ends in .github", func(t *testing.T) {
		repo := overrides(apacheRepository("org.github"))

		result := conformance.Check([]conformance.Repository{repo})

		if len(result.Violations) != 1 || result.Violations[0].Rule != conformance.RuleOverrides {
			t.Errorf("Violations = %+v, want one override violation", result.Violations)
		}
	})
}

func TestCheckSkipsForkArchivedAndEmptyRepositories(t *testing.T) {
	violating := func(name string) conformance.Repository {
		repo := apacheRepository(name)
		repo.Licence = "MIT"
		repo.Root = []conformance.Entry{file("CONTRIBUTING.md")}
		return repo
	}
	fork, archived, empty := violating("forked"), violating("archived"), violating("empty")
	fork.Fork = true
	archived.Archived = true
	empty.Empty = true
	all := violating("fork-and-archived")
	all.Fork, all.Archived, all.Empty = true, true, true

	t.Run("only skipped repositories", func(t *testing.T) {
		result := conformance.Check([]conformance.Repository{fork, archived, empty, all})

		if len(result.Violations) != 0 || result.Checked != 0 {
			t.Errorf("result = %+v, want no violations and nothing checked", result)
		}
	})

	t.Run("skipped repositories are not counted among checked ones", func(t *testing.T) {
		result := conformance.Check([]conformance.Repository{fork, apacheRepository("kept"), archived, empty})

		if len(result.Violations) != 0 || result.Checked != 1 {
			t.Errorf("result = %+v, want no violations and one repository checked", result)
		}
	})
}

func TestCheckLicence(t *testing.T) {
	cases := []struct {
		licence string
		want    string
	}{
		{"Apache-2.0", ""},
		{"MIT", "MIT detected, Apache-2.0 required"},
		{"", "none detected, Apache-2.0 required"},
		{"NOASSERTION", "NOASSERTION detected, Apache-2.0 required"},
		{"apache-2.0", "apache-2.0 detected, Apache-2.0 required"},
		{"Apache-2.0 ", "Apache-2.0  detected, Apache-2.0 required"},
		{"Apache-1.1", "Apache-1.1 detected, Apache-2.0 required"},
		{"GPL-3.0-only", "GPL-3.0-only detected, Apache-2.0 required"},
	}
	for _, tc := range cases {
		t.Run(tc.licence, func(t *testing.T) {
			repo := apacheRepository("project")
			repo.Licence = tc.licence

			result := conformance.Check([]conformance.Repository{repo})

			var want []conformance.Violation
			if tc.want != "" {
				want = []conformance.Violation{{Repository: "abrosimov/project", Rule: conformance.RuleLicence, Detail: tc.want}}
			}
			if !equalViolations(result.Violations, want) {
				t.Errorf("Violations = %+v, want %+v", result.Violations, want)
			}
		})
	}
}

func TestCheckOrdersViolationsByRepositoryThenLicenceBeforeOverride(t *testing.T) {
	violating := func(name string) conformance.Repository {
		repo := apacheRepository(name)
		repo.Licence = "MIT"
		repo.Root = []conformance.Entry{file("SECURITY.md")}
		return repo
	}
	clean := apacheRepository("middle")

	result := conformance.Check([]conformance.Repository{violating("zeta"), clean, violating("alpha")})

	want := []conformance.Violation{
		{Repository: "abrosimov/alpha", Rule: conformance.RuleLicence, Detail: "MIT detected, Apache-2.0 required"},
		{Repository: "abrosimov/alpha", Rule: conformance.RuleOverrides, Detail: "SECURITY.md"},
		{Repository: "abrosimov/zeta", Rule: conformance.RuleLicence, Detail: "MIT detected, Apache-2.0 required"},
		{Repository: "abrosimov/zeta", Rule: conformance.RuleOverrides, Detail: "SECURITY.md"},
	}
	if !equalViolations(result.Violations, want) {
		t.Errorf("Violations = %+v, want %+v", result.Violations, want)
	}
	if result.Checked != 3 {
		t.Errorf("Checked = %d, want 3", result.Checked)
	}
}

func TestCheckOfNoRepositoriesConforms(t *testing.T) {
	for name, repositories := range map[string][]conformance.Repository{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			result := conformance.Check(repositories)

			if len(result.Violations) != 0 || result.Checked != 0 {
				t.Errorf("result = %+v, want the zero result", result)
			}
		})
	}
}

func TestCheckReportsEveryViolationInOneRun(t *testing.T) {
	repositories := make([]conformance.Repository, 0, 10)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		repo := apacheRepository(name)
		repo.Licence = "BSD-3-Clause"
		repo.DotGitHub = []conformance.Entry{directory("ISSUE_TEMPLATE")}
		repositories = append(repositories, repo)
	}

	result := conformance.Check(repositories)

	if len(result.Violations) != 10 {
		t.Errorf("got %d violations, want 10: %+v", len(result.Violations), result.Violations)
	}
}

func TestViolationString(t *testing.T) {
	violation := conformance.Violation{
		Repository: "abrosimov/project",
		Rule:       conformance.RuleOverrides,
		Detail:     "CONTRIBUTING.md, docs/SECURITY.md",
	}

	got := violation.String()

	want := "abrosimov/project: community health override: CONTRIBUTING.md, docs/SECURITY.md"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	licence := conformance.Violation{Repository: "abrosimov/project", Rule: conformance.RuleLicence, Detail: "MIT detected"}
	if got, want := licence.String(), "abrosimov/project: licence: MIT detected"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestRepositorySkipped(t *testing.T) {
	cases := map[string]struct {
		repo conformance.Repository
		want bool
	}{
		"plain":    {conformance.Repository{}, false},
		"fork":     {conformance.Repository{Fork: true}, true},
		"archived": {conformance.Repository{Archived: true}, true},
		"empty":    {conformance.Repository{Empty: true}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.repo.Skipped(); got != tc.want {
				t.Errorf("Skipped() = %v, want %v", got, tc.want)
			}
		})
	}
}
