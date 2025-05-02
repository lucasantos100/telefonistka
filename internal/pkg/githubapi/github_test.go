package githubapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/wayfair-incubator/telefonistka/internal/pkg/argocd"
)

func TestGenerateSafePromotionBranchName(t *testing.T) {
	t.Parallel()
	prNumber := 11
	originBranch := "originBranch"
	targetPaths := []string{"targetPath1", "targetPath2"}
	result := generateSafePromotionBranchName(prNumber, originBranch, targetPaths)
	expectedResult := "promotions/11-originBranch-676f02019f18"
	if result != expectedResult {
		t.Errorf("Expected %s, got %s", expectedResult, result)
	}
}

// TestGenerateSafePromotionBranchNameLongBranchName tests the case where the original  branch name is longer than 250 characters
func TestGenerateSafePromotionBranchNameLongBranchName(t *testing.T) {
	t.Parallel()
	prNumber := 11

	originBranch := string(bytes.Repeat([]byte("originBranch"), 100))
	targetPaths := []string{"targetPath1", "targetPath2"}
	result := generateSafePromotionBranchName(prNumber, originBranch, targetPaths)
	if len(result) > 250 {
		t.Errorf("Expected branch name to be less than 250 characters, got %d", len(result))
	}
}

// TestGenerateSafePromotionBranchNameLongTargets tests the case where the target paths are longer than 250 characters
func TestGenerateSafePromotionBranchNameLongTargets(t *testing.T) {
	t.Parallel()
	prNumber := 11
	originBranch := "originBranch"
	targetPaths := []string{
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/1",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/2",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/3",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/4",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/5",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/6",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/7",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/8",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/9",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/10",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/11",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/12",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/13",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/14",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/15",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/16",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/17",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/18",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/19",
		"loooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooooong/target/path/20",
	}
	result := generateSafePromotionBranchName(prNumber, originBranch, targetPaths)
	if len(result) > 250 {
		t.Errorf("Expected branch name to be less than 250 characters, got %d", len(result))
	}
}

func TestAnalyzeCommentUpdateCheckBox(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		newBody                  string
		oldBody                  string
		checkboxIdentifier       string
		expectedWasCheckedBefore bool
		expectedIsCheckedNow     bool
	}{
		"Checkbox is marked": {
			oldBody: `This is a comment
foobar
- [ ] <!-- check-slug-1 --> Description of checkbox
foobar`,
			newBody: `This is a comment
foobar
- [x] <!-- check-slug-1 --> Description of checkbox
foobar`,
			checkboxIdentifier:       "check-slug-1",
			expectedWasCheckedBefore: false,
			expectedIsCheckedNow:     true,
		},
		"Checkbox is unmarked": {
			oldBody: `This is a comment
foobar
- [x] <!-- check-slug-1 --> Description of checkbox
foobar`,
			newBody: `This is a comment
foobar
- [ ] <!-- check-slug-1 --> Description of checkbox
foobar`,
			checkboxIdentifier:       "check-slug-1",
			expectedWasCheckedBefore: true,
			expectedIsCheckedNow:     false,
		},
		"Checkbox isn't in comment body": {
			oldBody: `This is a comment
foobar
foobar`,
			newBody: `This is a comment
foobar
foobar`,
			checkboxIdentifier:       "check-slug-1",
			expectedWasCheckedBefore: false,
			expectedIsCheckedNow:     false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wasCheckedBefore, isCheckedNow := analyzeCommentUpdateCheckBox(tc.newBody, tc.oldBody, tc.checkboxIdentifier)
			if isCheckedNow != tc.expectedIsCheckedNow {
				t.Errorf("%s: Expected isCheckedNow to be %v, got %v", name, tc.expectedIsCheckedNow, isCheckedNow)
			}
			if wasCheckedBefore != tc.expectedWasCheckedBefore {
				t.Errorf("%s: Expected wasCheckedBeforeto to be %v, got %v", name, tc.expectedWasCheckedBefore, wasCheckedBefore)
			}
		})
	}
}

func TestIsSyncFromBranchAllowedForThisPath(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		allowedPathRegex string
		path             string
		expectedResult   bool
	}{
		"Path is allowed": {
			allowedPathRegex: `^workspace/.*$`,
			path:             "workspace/app3",
			expectedResult:   true,
		},
		"Path is not allowed": {
			allowedPathRegex: `^workspace/.*$`,
			path:             "clusters/prod/aws/eu-east-1/app3",
			expectedResult:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := isSyncFromBranchAllowedForThisPath(tc.allowedPathRegex, tc.path)
			if result != tc.expectedResult {
				t.Errorf("%s: Expected result to be %v, got %v", name, tc.expectedResult, result)
			}
		})
	}
}

func TestGenerateArgoCdDiffComments(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		diffCommentDataTestDataFileName string
		expectedNumberOfComments        int
		maxCommentLength                int
	}{
		"All cluster diffs fit in one comment": {
			diffCommentDataTestDataFileName: "./testdata/diff_comment_data_test.json",
			expectedNumberOfComments:        1,
			maxCommentLength:                65535,
		},
		"Split diffs, one cluster per comment": {
			diffCommentDataTestDataFileName: "./testdata/diff_comment_data_test.json",
			expectedNumberOfComments:        3,
			maxCommentLength:                4000,
		},
		"Split diffs, but maxCommentLength is very small so need to use the concise template": {
			diffCommentDataTestDataFileName: "./testdata/diff_comment_data_test.json",
			expectedNumberOfComments:        3,
			maxCommentLength:                2000,
		},
	}

	if err := os.Setenv("TEMPLATES_PATH", "../../../templates/"); err != nil { //nolint:tenv
		t.Fatal(err)
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var diffCommentData DiffCommentData
			readJSONFromFile(t, tc.diffCommentDataTestDataFileName, &diffCommentData)

			result, err := generateArgoCdDiffComments(diffCommentData, tc.maxCommentLength)
			if err != nil {
				t.Fatalf("Error generating ArgoCD diff comments: %s", err)
			}
			for i, c := range result {
				t.Logf("comment %v length: %v", i, len(c))
			}
			if len(result) != tc.expectedNumberOfComments {
				t.Errorf("%s: Expected number of comments to be %v, got %v", name, tc.expectedNumberOfComments, len(result))
			}
			for _, comment := range result {
				if len(comment) > tc.maxCommentLength {
					t.Errorf("%s: Expected comment length to be less than %d, got %d", name, tc.maxCommentLength, len(comment))
				}
			}
		})
	}
}

func TestMarkdownGenerator(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		beConcise  bool
		partNumber int
		totalParts int
	}{
		"Basic templating": {
			beConcise:  false,
			partNumber: 0,
			totalParts: 0,
		},
		"Concice templeting": {
			beConcise:  true,
			partNumber: 0,
			totalParts: 0,
		},
		"Part of splitted comment ": {
			beConcise:  false,
			partNumber: 3,
			totalParts: 8,
		},
		"Unhealthy": {
			beConcise:  false,
			partNumber: 0,
			totalParts: 0,
		},
		"OutOfSync": {
			beConcise:  false,
			partNumber: 0,
			totalParts: 0,
		},
		"Temp app should not show sync or unhealthy warnings": {
			beConcise:  false,
			partNumber: 0,
			totalParts: 0,
		},
		"Show Sync from Branch checkbox": {
			beConcise:  false,
			partNumber: 0,
			totalParts: 0,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var diffCommentData DiffCommentData
			diffCommentDataTestDataFileName := "./testdata/data/" + t.Name() + ".json"
			expectedOutputContentFile := "./testdata/output/" + t.Name() + ".md"
			readJSONFromFile(t, diffCommentDataTestDataFileName, &diffCommentData)

			generatedMarkDownOutput, err := buildArgoCdDiffComment(diffCommentData, tc.beConcise, tc.partNumber, tc.totalParts)
			if err != nil {
				t.Fatalf("Error generating ArgoCD diff comments: %s", err)
			}

			// This is how I generate the expected test data
			// _ = os.WriteFile(expectedOutputContentFile, []byte(generatedMarkDownOutput), 0600)

			expectedOutputContent, err := os.ReadFile(expectedOutputContentFile)
			if err != nil {
				t.Fatalf("Error loading golden file: %s", err)
			}

			assert.Equal(t, generatedMarkDownOutput, string(expectedOutputContent))
		})
	}
}

func readJSONFromFile(t *testing.T, filename string, data interface{}) {
	t.Helper()
	// Read the JSON from the file
	jsonData, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("Error loading test data file: %s", err)
	}

	// Unserialize the JSON into the provided struct
	err = json.Unmarshal(jsonData, data)
	if err != nil {
		t.Fatalf("Error unmarshalling JSON: %s", err)
	}
}

func TestPrBody(t *testing.T) {
	t.Parallel()
	keys := []int{1, 2, 3}
	promotionSkipPaths := map[string]bool{"targetPath3": true}
	newPrMetadata := prMetadata{
		// note: "targetPath3" is missing from the list of promoted paths, so it should not
		// be included in the new PR body.
		PromotedPaths: []string{"targetPath1", "targetPath2", "targetPath4", "targetPath5", "targetPath6"},
		PreviousPromotionMetadata: map[int]promotionInstanceMetaData{
			1: {
				SourcePath:  "sourcePath1",
				TargetPaths: []string{"targetPath1", "targetPath2"},
			},
			2: {
				SourcePath:  "sourcePath2",
				TargetPaths: []string{"targetPath3", "targetPath4"},
			},
			3: {
				SourcePath:  "sourcePath3",
				TargetPaths: []string{"targetPath5", "targetPath6"},
			},
		},
	}
	newPrBody := prBody(keys, newPrMetadata, "", promotionSkipPaths)
	expectedPrBody, err := os.ReadFile("testdata/pr_body.golden.md")
	if err != nil {
		t.Fatalf("Error loading golden file: %s", err)
	}
	assert.Equal(t, string(expectedPrBody), newPrBody)
}

func TestPrBodyMultiComponent(t *testing.T) {
	t.Parallel()
	keys := []int{1, 2}
	promotionSkipPaths := map[string]bool{}
	newPrMetadata := prMetadata{
		// note: "targetPath3" is missing from the list of promoted paths, so it should not
		// be included in the new PR body.
		PromotedPaths: []string{"targetPath1/component1", "targetPath1/component2", "targetPath2/component1"},
		PreviousPromotionMetadata: map[int]promotionInstanceMetaData{
			1: {
				SourcePath:  "sourcePath1",
				TargetPaths: []string{"targetPath1"},
			},
			2: {
				SourcePath:  "sourcePath2",
				TargetPaths: []string{"targetPath2"},
			},
		},
	}
	newPrBody := prBody(keys, newPrMetadata, "", promotionSkipPaths)
	expectedPrBody, err := os.ReadFile("testdata/pr_body_multi_component.golden.md")
	if err != nil {
		t.Fatalf("Error loading golden file: %s", err)
	}
	assert.Equal(t, string(expectedPrBody), newPrBody)
}

func TestGhPrClientDetailsGetBlameURLPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Host      string
		Owner     string
		Repo      string
		ExpectURL string
	}{
		{
			"",
			"commercetools",
			"test",
			fmt.Sprintf("%s/commercetools/test/blame", githubPublicBaseURL),
		},
		{
			"https://myserver.github.com",
			"some-other-owner",
			"some-other-repo",
			"https://myserver.github.com/some-other-owner/some-other-repo/blame",
		},
	}

	// reset the GITHUB_HOST env to prevent conflicts with other tests.
	defer os.Unsetenv("GITHUB_HOST")

	for _, tc := range tests {
		os.Setenv("GITHUB_HOST", tc.Host)
		ghPrClientDetails := &GhPrClientDetails{Owner: tc.Owner, Repo: tc.Repo}
		blameURLPrefix := ghPrClientDetails.getBlameURLPrefix()
		assert.Equal(t, tc.ExpectURL, blameURLPrefix)
	}
}

func TestShouldSyncBranchCheckBoxBeDisplayed(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		componentPathList            []string
		allowSyncfromBranchPathRegex string
		diffOfChangedComponents      []argocd.DiffResult
		expected                     bool
	}{
		"New App": {
			componentPathList:            []string{"workspace/app1"},
			allowSyncfromBranchPathRegex: `^workspace/.*$`,
			diffOfChangedComponents: []argocd.DiffResult{
				{
					AppWasTemporarilyCreated: true,
					ComponentPath:            "workspace/app1",
				},
			},
			expected: false,
		},
		"App synced from branch": {
			componentPathList:            []string{"workspace/app1"},
			allowSyncfromBranchPathRegex: `^workspace/.*$`,
			diffOfChangedComponents: []argocd.DiffResult{
				{
					AppSyncedFromPRBranch: true,
					ComponentPath:         "workspace/app1",
				},
			},
			expected: false,
		},
		"Existing App": {
			componentPathList:            []string{"workspace/app1"},
			allowSyncfromBranchPathRegex: `^workspace/.*$`,
			diffOfChangedComponents: []argocd.DiffResult{
				{
					AppWasTemporarilyCreated: false,
					ComponentPath:            "workspace/app1",
				},
			},
			expected: true,
		},
		"Mixed New and Existing Apps": {
			componentPathList:            []string{"workspace/app1", "workspace/app2", "workspace/app3"},
			allowSyncfromBranchPathRegex: `^workspace/.*$`,
			diffOfChangedComponents: []argocd.DiffResult{
				{
					AppWasTemporarilyCreated: false,
					ComponentPath:            "workspace/app1",
				},
				{
					AppWasTemporarilyCreated: true,
					ComponentPath:            "workspace/app2",
				},
				{
					AppSyncedFromPRBranch: true,
					ComponentPath:         "workspace/app3",
				},
			},
			expected: true,
		},
	}

	for i, tc := range tests {
		result := shouldSyncBranchCheckBoxBeDisplayed(tc.componentPathList, tc.allowSyncfromBranchPathRegex, tc.diffOfChangedComponents)
		assert.Equal(t, tc.expected, result, i)
	}
}

func TestCommitStatusTargetURL(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		expectedURL   string
		templateFile  string
		validTemplate bool
	}{
		"Default URL when no env var is set": {
			expectedURL:   "https://github.com/wayfair-incubator/telefonistka",
			templateFile:  "",
			validTemplate: false,
		},
		"Custom URL from template": {
			expectedURL:   "https://custom-url.com?time=%d&calculated_time=%d",
			templateFile:  "./testdata/custom_commit_status_valid_template.gotmpl",
			validTemplate: true,
		},
		"Invalid template": {
			expectedURL:   "https://github.com/wayfair-incubator/telefonistka",
			templateFile:  "./testdata/custom_commit_status_invalid_template.gotmpl",
			validTemplate: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()

			expectedURL := tc.expectedURL
			if tc.templateFile != "" {
				if tc.validTemplate {
					expectedURL = fmt.Sprintf(expectedURL, now.UnixMilli(), now.Add(-10*time.Minute).UnixMilli())
				}
			}

			result := commitStatusTargetURL(now, tc.templateFile)
			if result != expectedURL {
				t.Errorf("%s: Expected URL to be %q, got %q", name, expectedURL, result)
			}
		})
	}
}

func Test_getPromotionSkipPaths(t *testing.T) {
	t.Parallel()
	type args struct {
		promotion PromotionInstance
	}
	tests := []struct {
		name string
		args args
		want map[string]bool
	}{
		{
			name: "No skip paths",
			args: args{
				promotion: PromotionInstance{
					Metadata: PromotionInstanceMetaData{
						PerComponentSkippedTargetPaths: map[string][]string{},
					},
				},
			},
			want: map[string]bool{},
		},
		{
			name: "one skip path",
			args: args{
				promotion: PromotionInstance{
					Metadata: PromotionInstanceMetaData{
						PerComponentSkippedTargetPaths: map[string][]string{
							"component1": {"targetPath1", "targetPath2"},
						},
					},
				},
			},
			want: map[string]bool{
				"targetPath1": true,
				"targetPath2": true,
			},
		},
		{
			name: "multiple skip path",
			args: args{
				promotion: PromotionInstance{
					Metadata: PromotionInstanceMetaData{
						PerComponentSkippedTargetPaths: map[string][]string{
							"component1": {"targetPath1", "targetPath2", "targetPath3"},
							"component2": {"targetPath3"},
							"component3": {"targetPath1", "targetPath2"},
						},
					},
				},
			},
			want: map[string]bool{
				"targetPath3": true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := getPromotionSkipPaths(tt.args.promotion)
			assert.Equal(t, tt.want, got)
		})
	}
}
