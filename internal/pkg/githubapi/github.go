package githubapi

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha1" //nolint:gosec // G505: Blocklisted import crypto/sha1: weak cryptographic primitive (gosec), this is not a cryptographic use case
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-github/v62/github"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/nao1215/markdown"
	log "github.com/sirupsen/logrus"
	"github.com/wayfair-incubator/telefonistka/internal/pkg/argocd"
	cfg "github.com/wayfair-incubator/telefonistka/internal/pkg/configuration"
	prom "github.com/wayfair-incubator/telefonistka/internal/pkg/prometheus"
	"golang.org/x/exp/maps"
)

const (
	githubCommentMaxSize = 65536
	githubPublicBaseURL  = "https://github.com"
)

type DiffCommentData struct {
	DiffOfChangedComponents   []argocd.DiffResult
	DisplaySyncBranchCheckBox bool
	BranchName                string
}

type promotionInstanceMetaData struct {
	SourcePath  string   `json:"sourcePath"`
	TargetPaths []string `json:"targetPaths"`
}

type GhPrClientDetails struct {
	GhClientPair *GhClientPair
	// This whole struct describe the metadata of the PR, so it makes sense to share the context with everything to generate HTTP calls related to that PR, right?
	Ctx           context.Context //nolint:containedctx
	DefaultBranch string
	Owner         string
	Repo          string
	PrAuthor      string
	PrNumber      int
	PrSHA         string
	Ref           string
	RepoURL       string
	PrLogger      *log.Entry
	Labels        []*github.Label
	PrMetadata    prMetadata
}

type prMetadata struct {
	OriginalPrAuthor          string                            `json:"originalPrAuthor"`
	OriginalPrNumber          int                               `json:"originalPrNumber"`
	PromotedPaths             []string                          `json:"promotedPaths"`
	PreviousPromotionMetadata map[int]promotionInstanceMetaData `json:"previousPromotionPaths"`
}

func (pm prMetadata) serialize() (string, error) {
	pmJson, err := json.Marshal(pm)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pmJson), nil
}

func (ghPrClientDetails *GhPrClientDetails) getPrMetadata(prBody string) {
	prMetadataRegex := regexp.MustCompile(`<!--\|.*\|(.*)\|-->`)
	serializedPrMetadata := prMetadataRegex.FindStringSubmatch(prBody)
	if len(serializedPrMetadata) == 2 {
		if serializedPrMetadata[1] != "" {
			ghPrClientDetails.PrLogger.Info("Found PR metadata")
			err := ghPrClientDetails.PrMetadata.DeSerialize(serializedPrMetadata[1])
			if err != nil {
				ghPrClientDetails.PrLogger.Errorf("Fail to parser PR metadata %v", err)
			}
		}
	}
}

func (ghPrClientDetails *GhPrClientDetails) getBlameURLPrefix() string {
	githubHost := getEnv("GITHUB_HOST", "")
	if githubHost == "" {
		githubHost = githubPublicBaseURL
	}
	return fmt.Sprintf("%s/%s/%s/blame", githubHost, ghPrClientDetails.Owner, ghPrClientDetails.Repo)
}

// shouldSyncBranchCheckBoxBeDisplayed checks if the sync branch checkbox should be displayed in the PR comment.
// The checkbox should be displayed if:
// - The component is allowed to be synced from a branch(based on Telefonsitka configuration)
// - The relevant app is not new, temporary app that was created just to generate the diff
func shouldSyncBranchCheckBoxBeDisplayed(componentPathList []string, allowSyncfromBranchPathRegex string, diffOfChangedComponents []argocd.DiffResult) bool {
	for _, componentPath := range componentPathList {
		// First we check if the component is allowed to be synced from a branch
		if !isSyncFromBranchAllowedForThisPath(allowSyncfromBranchPathRegex, componentPath) {
			continue
		}

		// Then we check the relevant app is not new, temporary app.
		// We don't support syncing new apps from branches
		for _, diffOfChangedComponent := range diffOfChangedComponents {
			if diffOfChangedComponent.ComponentPath == componentPath && !diffOfChangedComponent.AppWasTemporarilyCreated && !diffOfChangedComponent.AppSyncedFromPRBranch {
				return true
			}
		}
	}
	return false
}

func HandlePREvent(eventPayload *github.PullRequestEvent, ghPrClientDetails GhPrClientDetails, mainGithubClientPair GhClientPair, approverGithubClientPair GhClientPair, ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			ghPrClientDetails.PrLogger.Errorf("Recovered: %v", r)
		}
	}()

	ghPrClientDetails.getPrMetadata(eventPayload.PullRequest.GetBody())

	stat, ok := eventToHandle(eventPayload)
	if !ok {
		// nothing to do
		return
	}

	SetCommitStatus(ghPrClientDetails, "pending")

	var err error

	defer func() {
		if err != nil {
			SetCommitStatus(ghPrClientDetails, "error")
			return
		}
		SetCommitStatus(ghPrClientDetails, "success")
	}()

	switch stat {
	case "merged":
		err = handleMergedPrEvent(ghPrClientDetails, approverGithubClientPair.v3Client)
	case "changed":
		err = handleChangedPREvent(ctx, mainGithubClientPair, ghPrClientDetails, eventPayload)
	case "show-plan":
		err = handleShowPlanPREvent(ctx, ghPrClientDetails, eventPayload)
	}

	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Handling of PR event failed: err=%s\n", err)
	}
}

// eventToHandle returns the event to be handled, translated from a Github
// world into the Telefonistka world. If no event should be handled, ok is
// false.
func eventToHandle(eventPayload *github.PullRequestEvent) (event string, ok bool) {
	switch {
	case *eventPayload.Action == "closed" && *eventPayload.PullRequest.Merged:
		return "merged", true
	case *eventPayload.Action == "opened" || *eventPayload.Action == "reopened" || *eventPayload.Action == "synchronize":
		return "changed", true
	case *eventPayload.Action == "labeled" && DoesPrHasLabel(eventPayload.PullRequest.Labels, "show-plan"):
		return "show-plan", true
	default:
		return "", false
	}
}

func handleShowPlanPREvent(ctx context.Context, ghPrClientDetails GhPrClientDetails, eventPayload *github.PullRequestEvent) error {
	ghPrClientDetails.PrLogger.Infoln("Found show-plan label, posting plan")
	defaultBranch, _ := ghPrClientDetails.GetDefaultBranch()
	config, err := GetInRepoConfig(ghPrClientDetails, defaultBranch)
	if err != nil {
		return fmt.Errorf("get in-repo configuration: %w", err)
	}
	promotions, _ := GeneratePromotionPlan(ghPrClientDetails, config, *eventPayload.PullRequest.Head.Ref)
	commentPlanInPR(ghPrClientDetails, promotions)
	return nil
}

func handleChangedPREvent(ctx context.Context, mainGithubClientPair GhClientPair, ghPrClientDetails GhPrClientDetails, eventPayload *github.PullRequestEvent) error {
	botIdentity, _ := GetBotGhIdentity(mainGithubClientPair.v4Client, ctx)
	err := MimizeStalePrComments(ghPrClientDetails, mainGithubClientPair.v4Client, botIdentity)
	if err != nil {
		return fmt.Errorf("minimizing stale PR comments: %w", err)
	}
	defaultBranch, _ := ghPrClientDetails.GetDefaultBranch()
	config, err := GetInRepoConfig(ghPrClientDetails, defaultBranch)
	if err != nil {
		return fmt.Errorf("get in-repo configuration: %w", err)
	}
	if config.Argocd.CommentDiffonPR {
		componentPathList, err := generateListOfChangedComponentPaths(ghPrClientDetails, config)
		if err != nil {
			return fmt.Errorf("generate list of changed components: %w", err)
		}

		// Building a map component's path and a boolean value that indicates if we should diff it not.
		// I'm avoiding doing this in the ArgoCD package to avoid circular dependencies and keep package scope clean
		componentsToDiff := map[string]bool{}
		for _, componentPath := range componentPathList {
			c, err := getComponentConfig(ghPrClientDetails, componentPath, ghPrClientDetails.Ref)
			if err != nil {
				return fmt.Errorf("get component (%s) config:  %w", componentPath, err)
			}
			componentsToDiff[componentPath] = true
			if c.DisableArgoCDDiff {
				componentsToDiff[componentPath] = false
				ghPrClientDetails.PrLogger.Debugf("ArgoCD diff disabled for %s\n", componentPath)
			}
		}
		argoClients, err := argocd.CreateArgoCdClients()
		if err != nil {
			return fmt.Errorf("error creating ArgoCD clients: %w", err)
		}

		hasComponentDiff, hasComponentDiffErrors, diffOfChangedComponents, err := argocd.GenerateDiffOfChangedComponents(ctx, componentsToDiff, ghPrClientDetails.Ref, ghPrClientDetails.RepoURL, config.Argocd.UseSHALabelForAppDiscovery, config.Argocd.CreateTempAppObjectFroNewApps, argoClients)
		if err != nil {
			return fmt.Errorf("getting diff information: %w", err)
		}
		ghPrClientDetails.PrLogger.Debugf("Successfully got ArgoCD diff(comparing live objects against objects rendered form git ref %s)", ghPrClientDetails.Ref)
		if !hasComponentDiffErrors && !hasComponentDiff {
			ghPrClientDetails.PrLogger.Debugf("ArgoCD diff is empty, this PR will not change cluster state\n")
			prLables, resp, err := ghPrClientDetails.GhClientPair.v3Client.Issues.AddLabelsToIssue(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, *eventPayload.PullRequest.Number, []string{"noop"})
			prom.InstrumentGhCall(resp)
			if err != nil {
				ghPrClientDetails.PrLogger.Errorf("Could not label GitHub PR: err=%s\n%v\n", err, resp)
			} else {
				ghPrClientDetails.PrLogger.Debugf("PR %v labeled\n%+v", *eventPayload.PullRequest.Number, prLables)
			}
			// If the PR is a promotion PR and the diff is empty, we can auto-merge it
			// "len(componentPathList) > 0"  validates we are not auto-merging a PR that we failed to understand which apps it affects
			if DoesPrHasLabel(eventPayload.PullRequest.Labels, "promotion") && config.Argocd.AutoMergeNoDiffPRs && len(componentPathList) > 0 {
				ghPrClientDetails.PrLogger.Infof("Auto-merging (no diff) PR %d", *eventPayload.PullRequest.Number)
				err := MergePr(ghPrClientDetails, eventPayload.PullRequest.Number)
				if err != nil {
					return fmt.Errorf("PR auto merge: %w", err)
				}
			}
		}

		if len(diffOfChangedComponents) > 0 {
			diffCommentData := DiffCommentData{
				DiffOfChangedComponents: diffOfChangedComponents,
				BranchName:              ghPrClientDetails.Ref,
			}

			diffCommentData.DisplaySyncBranchCheckBox = shouldSyncBranchCheckBoxBeDisplayed(componentPathList, config.Argocd.AllowSyncfromBranchPathRegex, diffOfChangedComponents)
			componentsToDiffJSON, _ := json.Marshal(componentsToDiff)
			log.Infof("Generating ArgoCD Diff Comment for components: %+v, length of diff elements: %d", string(componentsToDiffJSON), len(diffCommentData.DiffOfChangedComponents))
			comments, err := generateArgoCdDiffComments(diffCommentData, githubCommentMaxSize)
			if err != nil {
				return fmt.Errorf("generate diff comment: %w", err)
			}
			for _, comment := range comments {
				err = commentPR(ghPrClientDetails, comment)
				if err != nil {
					return fmt.Errorf("commenting on PR: %w", err)
				}
			}
		} else {
			ghPrClientDetails.PrLogger.Debugf("Diff not find affected ArogCD apps")
		}
	}
	err = DetectDrift(ghPrClientDetails)
	if err != nil {
		return fmt.Errorf("detecting drift: %w", err)
	}
	return nil
}

func buildArgoCdDiffComment(diffCommentData DiffCommentData, beConcise bool, partNumber int, totalParts int) (string, error) {
	buf := new(bytes.Buffer)
	md := markdown.NewMarkdown(buf)
	const argoSmallLogo = `<img src="https://argo-cd.readthedocs.io/en/stable/assets/favicon.png" width="20"/>`
	if partNumber != 0 {
		md.PlainTextf("Component %d/%d: %s (Split for comment size)\n", partNumber, totalParts, diffCommentData.DiffOfChangedComponents[0].ComponentPath)
	}
	if !beConcise {
		md.PlainText("Diff of ArgoCD applications:\n")
	} else {
		md.PlainText("Diff of ArgoCD applications (concise view, full diff didn't fit GH comment):\n")
	}
	for _, appDiffResult := range diffCommentData.DiffOfChangedComponents {
		if appDiffResult.DiffError != nil {
			md.Cautionf("%s (%s) ", markdown.Bold("Error getting diff from ArgoCD"), markdown.Code(appDiffResult.ComponentPath))
			md.PlainTextf("Please check the App Conditions of %s %s for more details.", argoSmallLogo, markdown.Bold(markdown.Link(appDiffResult.ArgoCdAppName, appDiffResult.ArgoCdAppURL)))
			if appDiffResult.AppWasTemporarilyCreated {
				md.Warning("For investigation we kept the temporary application, please make sure to clean it up later!")
			}
			md.CodeBlocks(markdown.SyntaxHighlightNone, appDiffResult.DiffError.Error())
		} else {
			md.PlainTextf("%s %s @ %s", argoSmallLogo, markdown.Bold(markdown.Link(appDiffResult.ArgoCdAppName, appDiffResult.ArgoCdAppURL)), markdown.Code(appDiffResult.ComponentPath))

			// If the app was temporarily created, we should inform the user about it, if not we should inform about "unusual" health and sync status
			if appDiffResult.AppWasTemporarilyCreated {
				md.Note("Telefonistka has temporarily created an ArgoCD app object to render manifest previews.  \n> Please be aware:  \n> * The app will only appear in the ArgoCD UI for a few seconds.")
			} else {
				if appDiffResult.ArgoCdAppHealthStatus != "Healthy" {
					md.Cautionf("The ArgoCD app health status is currently %s", appDiffResult.ArgoCdAppHealthStatus)
				}
				if appDiffResult.ArgoCdAppSyncStatus != "Synced" {
					md.Warningf("The ArgoCD app sync status is currently %s", appDiffResult.ArgoCdAppSyncStatus)
				}
				if !appDiffResult.ArgoCdAppAutoSyncEnabled {
					md.Note("This ArgoCD app is doesn't have `auto-sync` enabled, merging this PR will **not** apply changes to cluster without additional actions.")
				}
			}
			if appDiffResult.HasDiff {
				md.PlainText("\n<details><summary>ArgoCD Diff(Click to expand):</summary>\n\n```diff\n")
				for _, objectDiff := range appDiffResult.DiffElements {
					if objectDiff.Diff != "" {
						if !beConcise {
							md.PlainTextf("%s/%s/%s:\n%s", objectDiff.ObjectNamespace, objectDiff.ObjectKind, objectDiff.ObjectName, objectDiff.Diff)
						} else {
							md.PlainTextf("%s/%s/%s", objectDiff.ObjectNamespace, objectDiff.ObjectKind, objectDiff.ObjectName)
						}
					}
				}
				md.PlainText("\n\n```\n\n</details>\n")
			} else {
				if appDiffResult.AppSyncedFromPRBranch {
					md.Note("The app already has this branch set as the source target revision, and autosync is enabled. Diff calculation was skipped.")
				} else {
					md.PlainText("No diff 🤷")
				}
			}
		}
	}
	if diffCommentData.DisplaySyncBranchCheckBox {
		md.PlainTextf("- [ ] <!-- telefonistka-argocd-branch-sync --> Set ArgoCD apps Target Revision to `%s`", diffCommentData.BranchName)
	}
	err := md.Build()
	return buf.String(), err
}

func generateArgoCdDiffComments(diffCommentData DiffCommentData, githubCommentMaxSize int) (comments []string, err error) {
	commentBody, err := buildArgoCdDiffComment(diffCommentData, false, 0, 0)
	if err != nil {
		log.Errorf("Failed to build ArgoCD diff comment: err=%s\n", err)
		return comments, err
	}

	// Happy path, the diff comment is small enough to be posted in one comment
	if len(commentBody) < githubCommentMaxSize {
		comments = append(comments, commentBody)
		return comments, nil
	}

	// If the diff comment is too large, we'll split it into multiple comments, one per component
	totalComponents := len(diffCommentData.DiffOfChangedComponents)
	for i, singleComponentDiff := range diffCommentData.DiffOfChangedComponents {
		componentTemplateData := diffCommentData
		componentTemplateData.DiffOfChangedComponents = []argocd.DiffResult{singleComponentDiff}
		commentBody, err := buildArgoCdDiffComment(diffCommentData, false, i+1, totalComponents)
		if err != nil {
			log.Errorf("Failed to build ArgoCD diff comment: err=%s\n", err)
			return comments, err
		}

		// Even per component comments can be too large, in that case we'll just use the concise template
		// Somewhat Happy path, the per-component diff comment is small enough to be posted in one comment
		if len(commentBody) < githubCommentMaxSize {
			comments = append(comments, commentBody)
			continue
		}

		// now we don't have much choice, this is the saddest path, we'll use the concise template
		commentBody, err = buildArgoCdDiffComment(diffCommentData, true, i+1, totalComponents)
		if err != nil {
			log.Errorf("Failed to build ArgoCD diff comment: err=%s\n", err)
			return comments, err
		}
		comments = append(comments, commentBody)
	}

	return comments, nil
}

// ReciveEventFile this one is similar to ReciveWebhook but it's used for CLI triggering, i  simulates a webhook event to use the same code path as the webhook handler.
func ReciveEventFile(eventType string, eventFilePath string, mainGhClientCache *lru.Cache[string, GhClientPair], prApproverGhClientCache *lru.Cache[string, GhClientPair]) {
	log.Infof("Event type: %s", eventType)
	log.Infof("Proccesing file: %s", eventFilePath)

	payload, err := os.ReadFile(eventFilePath)
	if err != nil {
		panic(err)
	}
	eventPayloadInterface, err := github.ParseWebHook(eventType, payload)
	if err != nil {
		log.Errorf("could not parse webhook: err=%s\n", err)
		prom.InstrumentWebhookHit("parsing_failed")
		return
	}
	r, _ := http.NewRequest("POST", "", nil) //nolint:noctx
	r.Body = io.NopCloser(bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-GitHub-Event", eventType)

	handleEvent(eventPayloadInterface, mainGhClientCache, prApproverGhClientCache, r, payload)
}

// ReciveWebhook is the main entry point for the webhook handling it starts parases the webhook payload and start a thread to handle the event success/failure are dependant on the payload parsing only
func ReciveWebhook(r *http.Request, mainGhClientCache *lru.Cache[string, GhClientPair], prApproverGhClientCache *lru.Cache[string, GhClientPair], githubWebhookSecret []byte) error {
	payload, err := github.ValidatePayload(r, githubWebhookSecret)
	if err != nil {
		log.Errorf("error reading request body: err=%s\n", err)
		prom.InstrumentWebhookHit("validation_failed")
		return err
	}
	eventType := github.WebHookType(r)

	eventPayloadInterface, err := github.ParseWebHook(eventType, payload)
	if err != nil {
		log.Errorf("could not parse webhook: err=%s\n", err)
		prom.InstrumentWebhookHit("parsing_failed")
		return err
	}
	prom.InstrumentWebhookHit("successful")

	go handleEvent(eventPayloadInterface, mainGhClientCache, prApproverGhClientCache, r, payload)
	return nil
}

func handleEvent(eventPayloadInterface interface{}, mainGhClientCache *lru.Cache[string, GhClientPair], prApproverGhClientCache *lru.Cache[string, GhClientPair], r *http.Request, payload []byte) {
	// We don't use the request context as it might have a short deadline and we don't want to stop event handling based on that
	// But we do want to stop the event handling after a certain point, so:
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var mainGithubClientPair GhClientPair
	var approverGithubClientPair GhClientPair

	log.Infof("Handling event type %T", eventPayloadInterface)

	switch eventPayload := eventPayloadInterface.(type) {
	case *github.PushEvent:
		// this is a commit push, do something with it?
		repoOwner := *eventPayload.Repo.Owner.Login
		mainGithubClientPair.GetAndCache(mainGhClientCache, "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_PATH", "GITHUB_OAUTH_TOKEN", repoOwner, ctx)

		prLogger := log.WithFields(log.Fields{
			"event_type": "push",
		})

		ghPrClientDetails := GhPrClientDetails{
			Ctx:          ctx,
			GhClientPair: &mainGithubClientPair,
			Owner:        repoOwner,
			Repo:         *eventPayload.Repo.Name,
			RepoURL:      *eventPayload.Repo.HTMLURL,
			PrLogger:     prLogger,
		}

		handlePushEvent(ctx, eventPayload, r, payload, ghPrClientDetails)
	case *github.PullRequestEvent:
		log.Infof("is PullRequestEvent(%s)", *eventPayload.Action)

		prLogger := log.WithFields(log.Fields{
			"repo":       *eventPayload.Repo.Owner.Login + "/" + *eventPayload.Repo.Name,
			"prNumber":   *eventPayload.PullRequest.Number,
			"event_type": "pr",
		})

		repoOwner := *eventPayload.Repo.Owner.Login

		mainGithubClientPair.GetAndCache(mainGhClientCache, "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_PATH", "GITHUB_OAUTH_TOKEN", repoOwner, ctx)
		approverGithubClientPair.GetAndCache(prApproverGhClientCache, "APPROVER_GITHUB_APP_ID", "APPROVER_GITHUB_APP_PRIVATE_KEY_PATH", "APPROVER_GITHUB_OAUTH_TOKEN", repoOwner, ctx)

		ghPrClientDetails := GhPrClientDetails{
			Ctx:          ctx,
			GhClientPair: &mainGithubClientPair,
			Labels:       eventPayload.PullRequest.Labels,
			Owner:        repoOwner,
			Repo:         *eventPayload.Repo.Name,
			RepoURL:      *eventPayload.Repo.HTMLURL,
			PrNumber:     *eventPayload.PullRequest.Number,
			Ref:          *eventPayload.PullRequest.Head.Ref,
			PrAuthor:     *eventPayload.PullRequest.User.Login,
			PrLogger:     prLogger,
			PrSHA:        *eventPayload.PullRequest.Head.SHA,
		}

		HandlePREvent(eventPayload, ghPrClientDetails, mainGithubClientPair, approverGithubClientPair, ctx)

	case *github.IssueCommentEvent:
		repoOwner := *eventPayload.Repo.Owner.Login
		mainGithubClientPair.GetAndCache(mainGhClientCache, "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_PATH", "GITHUB_OAUTH_TOKEN", repoOwner, ctx)

		botIdentity, _ := GetBotGhIdentity(mainGithubClientPair.v4Client, ctx)
		prLogger := log.WithFields(log.Fields{
			"repo":       *eventPayload.Repo.Owner.Login + "/" + *eventPayload.Repo.Name,
			"prNumber":   *eventPayload.Issue.Number,
			"event_type": "issue_comment",
		})
		// Ignore comment events sent by the bot (this is about who trigger the event not who wrote the comment)
		if *eventPayload.Sender.Login != botIdentity {
			ghPrClientDetails := GhPrClientDetails{
				Ctx:          ctx,
				GhClientPair: &mainGithubClientPair,
				Owner:        repoOwner,
				Repo:         *eventPayload.Repo.Name,
				RepoURL:      *eventPayload.Repo.HTMLURL,
				PrNumber:     *eventPayload.Issue.Number,
				PrAuthor:     *eventPayload.Issue.User.Login,
				PrLogger:     prLogger,
			}
			_ = handleCommentPrEvent(ghPrClientDetails, eventPayload, botIdentity)
		} else {
			log.Debug("Ignoring self comment")
		}

	default:
		return
	}
}

func analyzeCommentUpdateCheckBox(newBody string, oldBody string, checkboxIdentifier string) (wasCheckedBefore bool, isCheckedNow bool) {
	checkboxPattern := fmt.Sprintf(`(?m)^\s*-\s*\[(.)\]\s*<!-- %s -->.*$`, checkboxIdentifier)
	checkBoxRegex := regexp.MustCompile(checkboxPattern)
	oldCheckBoxContent := checkBoxRegex.FindStringSubmatch(oldBody)
	newCheckBoxContent := checkBoxRegex.FindStringSubmatch(newBody)

	// I'm grabbing the second group of the regex, which is the checkbox content (either "x" or " ")
	// The first element of the result is the whole match
	if len(newCheckBoxContent) < 2 || len(oldCheckBoxContent) < 2 {
		return false, false
	}
	if len(newCheckBoxContent) >= 2 {
		if newCheckBoxContent[1] == "x" {
			isCheckedNow = true
		}
	}

	if len(oldCheckBoxContent) >= 2 {
		if oldCheckBoxContent[1] == "x" {
			wasCheckedBefore = true
		}
	}

	return
}

func isSyncFromBranchAllowedForThisPath(allowedPathRegex string, path string) bool {
	allowedPathsRegex := regexp.MustCompile(allowedPathRegex)
	return allowedPathsRegex.MatchString(path)
}

func handleCommentPrEvent(ghPrClientDetails GhPrClientDetails, ce *github.IssueCommentEvent, botIdentity string) error {
	defaultBranch, _ := ghPrClientDetails.GetDefaultBranch()
	config, err := GetInRepoConfig(ghPrClientDetails, defaultBranch)
	if err != nil {
		return err
	}
	// Comment events doesn't have Ref/SHA in payload, enriching the object:
	_, _ = ghPrClientDetails.GetRef()
	_, _ = ghPrClientDetails.GetSHA()

	// This part should only happen on edits of bot comments on open PRs (I'm not testing Issue vs PR as Telefonsitka only creates PRs at this point)
	if *ce.Action == "edited" && *ce.Comment.User.Login == botIdentity && *ce.Issue.State == "open" {
		const checkboxIdentifier = "telefonistka-argocd-branch-sync"
		checkboxWaschecked, checkboxIsChecked := analyzeCommentUpdateCheckBox(*ce.Comment.Body, *ce.Changes.Body.From, checkboxIdentifier)
		if !checkboxWaschecked && checkboxIsChecked {
			ghPrClientDetails.PrLogger.Infof("Sync Checkbox was checked")
			if config.Argocd.AllowSyncfromBranchPathRegex != "" {
				ghPrClientDetails.getPrMetadata(ce.Issue.GetBody())
				componentPathList, err := generateListOfChangedComponentPaths(ghPrClientDetails, config)
				if err != nil {
					ghPrClientDetails.PrLogger.Errorf("Failed to get list of changed components: err=%s\n", err)
				}

				for _, componentPath := range componentPathList {
					if isSyncFromBranchAllowedForThisPath(config.Argocd.AllowSyncfromBranchPathRegex, componentPath) {
						err := argocd.SetArgoCDAppRevision(ghPrClientDetails.Ctx, componentPath, ghPrClientDetails.Ref, ghPrClientDetails.RepoURL, config.Argocd.UseSHALabelForAppDiscovery)
						if err != nil {
							ghPrClientDetails.PrLogger.Errorf("Failed to sync ArgoCD app from branch: err=%s\n", err)
						}
					}
				}
			}
		}
	}

	// I should probably deprecated this whole part altogether - it was designed to solve a *very* specific problem that is probably no longer relevant with GitHub Rulesets
	// The only reason I'm keeping it is that I don't have a clear feature depreciation policy and if I do remove it should be in a distinct PR
	for commentSubstring, commitStatusContext := range config.ToggleCommitStatus {
		if strings.Contains(*ce.Comment.Body, "/"+commentSubstring) {
			err := ghPrClientDetails.ToggleCommitStatus(commitStatusContext, *ce.Sender.Name)
			if err != nil {
				ghPrClientDetails.PrLogger.Errorf("Failed to toggle %s context,  err=%v", commitStatusContext, err)
				break
			} else {
				ghPrClientDetails.PrLogger.Infof("Toggled %s status", commitStatusContext)
			}
		}
	}
	return err
}

func commentPlanInPR(ghPrClientDetails GhPrClientDetails, promotions map[string]PromotionInstance) {
	templateOutput, err := executeTemplate("dryRunMsg", defaultTemplatesFullPath("dry-run-pr-comment.gotmpl"), promotions)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Failed to generate dry-run comment template: err=%s\n", err)
		return
	}
	_ = commentPR(ghPrClientDetails, templateOutput)
}

func executeTemplate(templateName string, templateFile string, data interface{}) (string, error) {
	var templateOutput bytes.Buffer
	messageTemplate, err := template.New(templateName).ParseFiles(templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}
	err = messageTemplate.ExecuteTemplate(&templateOutput, templateName, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}
	return templateOutput.String(), nil
}

func defaultTemplatesFullPath(templateFile string) string {
	return filepath.Join(getEnv("TEMPLATES_PATH", "templates/") + templateFile)
}

func commentPR(ghPrClientDetails GhPrClientDetails, commentBody string) error {
	err := ghPrClientDetails.CommentOnPr(commentBody)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Failed to comment in PR: err=%v", err)
		return err
	}
	return nil
}

func BumpVersion(ghPrClientDetails GhPrClientDetails, defaultBranch string, filePath string, newFileContent string, triggeringRepo string, triggeringRepoSHA string, triggeringActor string, autoMerge bool) error {
	var treeEntries []*github.TreeEntry

	generateBumpTreeEntiesForCommit(&treeEntries, ghPrClientDetails, defaultBranch, filePath, newFileContent)

	commit, err := createCommit(ghPrClientDetails, treeEntries, defaultBranch, "Bumping version @ "+filePath)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Commit creation failed: err=%v", err)
		return err
	}
	newBranchRef, err := createBranch(ghPrClientDetails, commit, "artifact_version_bump/"+triggeringRepo+"/"+triggeringRepoSHA) // TODO figure out branch name!!!!
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Branch creation failed: err=%v", err)
		return err
	}

	newPrTitle := triggeringRepo + "🚠 Bumping version @ " + filePath
	newPrBody := fmt.Sprintf("Bumping version triggered by %s@%s", triggeringRepo, triggeringRepoSHA)
	pr, err := createPrObject(ghPrClientDetails, newBranchRef, newPrTitle, newPrBody, defaultBranch, triggeringActor)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("PR opening failed: err=%v", err)
		return err
	}

	ghPrClientDetails.PrLogger.Infof("New PR URL: %s", *pr.HTMLURL)

	if autoMerge {
		ghPrClientDetails.PrLogger.Infof("Auto-merging PR %d", *pr.Number)
		err := MergePr(ghPrClientDetails, pr.Number)
		if err != nil {
			ghPrClientDetails.PrLogger.Errorf("PR auto merge failed: err=%v", err)
			return err
		}
	}

	return nil
}

func handleMergedPrEvent(ghPrClientDetails GhPrClientDetails, prApproverGithubClient *github.Client) error {
	defaultBranch, _ := ghPrClientDetails.GetDefaultBranch()
	config, err := GetInRepoConfig(ghPrClientDetails, defaultBranch)
	if err != nil {
		_ = ghPrClientDetails.CommentOnPr(fmt.Sprintf("Failed to get configuration\n```\n%s\n```\n", err))
		return err
	}

	// configBranch = default branch as the PR is closed at this and its branch deleted.
	// If we'l ever want to generate this plan on an unmerged PR the PR branch (ghPrClientDetails.Ref) should be used
	promotions, _ := GeneratePromotionPlan(ghPrClientDetails, config, defaultBranch)
	if !config.DryRunMode {
		for _, promotion := range promotions {
			// TODO this whole part shouldn't be in main, but I need to refactor some circular dep's

			// because I use GitHub low level (tree) API the order of operation is somewhat different compared to regular git CLI flow:
			// I create the sync commit against HEAD, create a new branch based on that commit and finally open a PR based on that branch

			var treeEntries []*github.TreeEntry
			for trgt, src := range promotion.ComputedSyncPaths {
				err = GenerateSyncTreeEntriesForCommit(&treeEntries, ghPrClientDetails, src, trgt, defaultBranch)
				if err != nil {
					ghPrClientDetails.PrLogger.Errorf("Failed to generate treeEntries for %s > %s,  err=%v", src, trgt, err)
				} else {
					ghPrClientDetails.PrLogger.Debugf("Generated treeEntries for %s > %s", src, trgt)
				}
			}

			if len(treeEntries) < 1 {
				ghPrClientDetails.PrLogger.Infof("TreeEntries list is empty")
				continue
			}

			commit, err := createCommit(ghPrClientDetails, treeEntries, defaultBranch, "Syncing from "+promotion.Metadata.SourcePath)
			if err != nil {
				ghPrClientDetails.PrLogger.Errorf("Commit creation failed: err=%v", err)
				return err
			}

			newBranchName := generateSafePromotionBranchName(ghPrClientDetails.PrNumber, ghPrClientDetails.Ref, promotion.Metadata.TargetPaths)

			newBranchRef, err := createBranch(ghPrClientDetails, commit, newBranchName)
			if err != nil {
				ghPrClientDetails.PrLogger.Errorf("Branch creation failed: err=%v", err)
				return err
			}

			components := strings.Join(promotion.Metadata.ComponentNames, ",")
			newPrTitle := fmt.Sprintf("🚀 Promotion: %s ➡️  %s", components, promotion.Metadata.TargetDescription)

			var originalPrAuthor string
			// If the triggering PR was opened manually and it doesn't include in-body metadata, use the PR author
			// If the triggering PR as opened by Telefonistka and it has in-body metadata, fetch the original author from there
			if ghPrClientDetails.PrMetadata.OriginalPrAuthor != "" {
				originalPrAuthor = ghPrClientDetails.PrMetadata.OriginalPrAuthor
			} else {
				originalPrAuthor = ghPrClientDetails.PrAuthor
			}

			newPrBody := generatePromotionPrBody(ghPrClientDetails, components, promotion, originalPrAuthor)

			pull, err := createPrObject(ghPrClientDetails, newBranchRef, newPrTitle, newPrBody, defaultBranch, originalPrAuthor)
			if err != nil {
				ghPrClientDetails.PrLogger.Errorf("PR opening failed: err=%v", err)
				return err
			}
			if config.AutoApprovePromotionPrs {
				err := ApprovePr(prApproverGithubClient, ghPrClientDetails, pull.Number)
				if err != nil {
					ghPrClientDetails.PrLogger.Errorf("PR auto approval failed: err=%v", err)
					return err
				}
			}
			if promotion.Metadata.AutoMerge {
				ghPrClientDetails.PrLogger.Infof("Auto-merging PR %d", *pull.Number)
				templateData := map[string]interface{}{
					"prNumber": *pull.Number,
				}
				templateOutput, err := executeTemplate("autoMerge", defaultTemplatesFullPath("auto-merge-comment.gotmpl"), templateData)
				if err != nil {
					return err
				}
				err = commentPR(ghPrClientDetails, templateOutput)
				if err != nil {
					return err
				}

				err = MergePr(ghPrClientDetails, pull.Number)
				if err != nil {
					ghPrClientDetails.PrLogger.Errorf("PR auto merge failed: err=%v", err)
					return err
				}
			}
		}
	} else {
		commentPlanInPR(ghPrClientDetails, promotions)
	}

	if config.Argocd.AllowSyncfromBranchPathRegex != "" {
		componentPathList, err := generateListOfChangedComponentPaths(ghPrClientDetails, config)
		if err != nil {
			ghPrClientDetails.PrLogger.Errorf("Failed to get list of changed components for setting ArgoCD app targetRef to HEAD: err=%s\n", err)
		}
		for _, componentPath := range componentPathList {
			if isSyncFromBranchAllowedForThisPath(config.Argocd.AllowSyncfromBranchPathRegex, componentPath) {
				ghPrClientDetails.PrLogger.Infof("Ensuring ArgoCD app %s is set to HEAD\n", componentPath)
				err := argocd.SetArgoCDAppRevision(ghPrClientDetails.Ctx, componentPath, "HEAD", ghPrClientDetails.RepoURL, config.Argocd.UseSHALabelForAppDiscovery)
				if err != nil {
					ghPrClientDetails.PrLogger.Errorf("Failed to set ArgoCD app @  %s, to HEAD: err=%s\n", componentPath, err)
				}
			}
		}
	}

	return err
}

// Creating a unique branch name based on the PR number, PR ref and the promotion target paths
// Max length of branch name is 250 characters
func generateSafePromotionBranchName(prNumber int, originalBranchName string, targetPaths []string) string {
	targetPathsBa := []byte(strings.Join(targetPaths, "_"))
	hasher := sha1.New() //nolint:gosec // G505: Blocklisted import crypto/sha1: weak cryptographic primitive (gosec), this is not a cryptographic use case
	hasher.Write(targetPathsBa)
	uniqBranchNameSuffix := firstN(hex.EncodeToString(hasher.Sum(nil)), 12)
	safeOriginalBranchName := firstN(strings.Replace(originalBranchName, "/", "-", -1), 200)
	return fmt.Sprintf("promotions/%v-%v-%v", prNumber, safeOriginalBranchName, uniqBranchNameSuffix)
}

func firstN(str string, n int) string {
	v := []rune(str)
	if n >= len(v) {
		return str
	}
	return string(v[:n])
}

func MergePr(details GhPrClientDetails, number *int) error {
	operation := func() error {
		err := tryMergePR(details, number)
		if err != nil {
			if isMergeErrorRetryable(err.Error()) {
				if err != nil {
					details.PrLogger.Warnf("Failed to merge PR: transient err=%v", err)
				}
				return err
			}
			details.PrLogger.Errorf("Failed to merge PR: permanent err=%v", err)
			return backoff.Permanent(err)
		}
		return nil
	}

	// Using default values, see https://pkg.go.dev/github.com/cenkalti/backoff#pkg-constants
	err := backoff.Retry(operation, backoff.NewExponentialBackOff())
	if err != nil {
		details.PrLogger.Errorf("Failed to merge PR: backoff err=%v", err)
	}

	return err
}

func tryMergePR(details GhPrClientDetails, number *int) error {
	_, resp, err := details.GhClientPair.v3Client.PullRequests.Merge(details.Ctx, details.Owner, details.Repo, *number, "Auto-merge", nil)
	prom.InstrumentGhCall(resp)
	return err
}

func isMergeErrorRetryable(errMessage string) bool {
	return strings.Contains(errMessage, "405") && strings.Contains(errMessage, "try the merge again")
}

func (pm *prMetadata) DeSerialize(s string) error {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	err = json.Unmarshal(decoded, pm)
	return err
}

func (p GhPrClientDetails) CommentOnPr(commentBody string) error {
	commentBody = "<!-- telefonistka_tag -->\n" + commentBody

	comment := &github.IssueComment{Body: &commentBody}
	_, resp, err := p.GhClientPair.v3Client.Issues.CreateComment(p.Ctx, p.Owner, p.Repo, p.PrNumber, comment)
	prom.InstrumentGhCall(resp)
	if err != nil {
		p.PrLogger.Errorf("Could not comment in PR: err=%s\n%v\n", err, resp)
	}
	return err
}

func DoesPrHasLabel(labels []*github.Label, name string) bool {
	for _, l := range labels {
		if *l.Name == name {
			return true
		}
	}
	return false
}

func (p *GhPrClientDetails) ToggleCommitStatus(context string, user string) error {
	var r error
	listOpts := &github.ListOptions{}

	initialStatuses, resp, err := p.GhClientPair.v3Client.Repositories.ListStatuses(p.Ctx, p.Owner, p.Repo, p.Ref, listOpts)
	prom.InstrumentGhCall(resp)
	if err != nil {
		p.PrLogger.Errorf("Failed to fetch  existing statuses for commit  %s, err=%s", p.Ref, err)
		r = err
	}

	for _, commitStatus := range initialStatuses {
		if *commitStatus.Context == context {
			if *commitStatus.State != "success" {
				p.PrLogger.Infof("%s Toggled  %s(%s) to success", user, context, *commitStatus.State)
				*commitStatus.State = "success"
				_, resp, err := p.GhClientPair.v3Client.Repositories.CreateStatus(p.Ctx, p.Owner, p.Repo, p.PrSHA, commitStatus)
				prom.InstrumentGhCall(resp)
				if err != nil {
					p.PrLogger.Errorf("Failed to create context %s, err=%s", context, err)
					r = err
				}
			} else {
				p.PrLogger.Infof("%s Toggled %s(%s) to failure", user, context, *commitStatus.State)
				*commitStatus.State = "failure"
				_, resp, err := p.GhClientPair.v3Client.Repositories.CreateStatus(p.Ctx, p.Owner, p.Repo, p.PrSHA, commitStatus)
				prom.InstrumentGhCall(resp)
				if err != nil {
					p.PrLogger.Errorf("Failed to create context %s, err=%s", context, err)
					r = err
				}
			}
			break
		}
	}

	return r
}

func SetCommitStatus(ghPrClientDetails GhPrClientDetails, state string) {
	// TODO change all these values
	tcontext := "telefonistka"
	avatarURL := "https://avatars.githubusercontent.com/u/1616153?s=64"
	description := "Telefonistka GitOps Bot"
	tmplFile := os.Getenv("CUSTOM_COMMIT_STATUS_URL_TEMPLATE_PATH")

	targetURL := commitStatusTargetURL(time.Now(), tmplFile)

	commitStatus := &github.RepoStatus{
		TargetURL:   &targetURL,
		Description: &description,
		State:       &state,
		Context:     &tcontext,
		AvatarURL:   &avatarURL,
	}
	ghPrClientDetails.PrLogger.Debugf("Setting commit %s status to %s", ghPrClientDetails.PrSHA, state)

	// use a separate context to avoid event processing timeout to cause
	// failures in updating the commit status
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, resp, err := ghPrClientDetails.GhClientPair.v3Client.Repositories.CreateStatus(ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, ghPrClientDetails.PrSHA, commitStatus)
	prom.InstrumentGhCall(resp)
	repoSlug := ghPrClientDetails.Owner + "/" + ghPrClientDetails.Repo
	prom.IncCommitStatusUpdateCounter(repoSlug, state)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Failed to set commit status: err=%s\n%v", err, resp)
	}
}

func (p *GhPrClientDetails) GetSHA() (string, error) {
	if p.PrSHA == "" {
		prObject, resp, err := p.GhClientPair.v3Client.PullRequests.Get(p.Ctx, p.Owner, p.Repo, p.PrNumber)
		prom.InstrumentGhCall(resp)
		if err != nil {
			p.PrLogger.Errorf("Could not get pr data: err=%s\n%v\n", err, resp)
			return "", err
		}
		p.PrSHA = *prObject.Head.SHA
		return p.PrSHA, err
	} else {
		return p.PrSHA, nil
	}
}

func (p *GhPrClientDetails) GetRef() (string, error) {
	if p.Ref == "" {
		prObject, resp, err := p.GhClientPair.v3Client.PullRequests.Get(p.Ctx, p.Owner, p.Repo, p.PrNumber)
		prom.InstrumentGhCall(resp)
		if err != nil {
			p.PrLogger.Errorf("Could not get pr data: err=%s\n%v\n", err, resp)
			return "", err
		}
		p.Ref = *prObject.Head.Ref
		return p.Ref, err
	} else {
		return p.Ref, nil
	}
}

func (p *GhPrClientDetails) GetDefaultBranch() (string, error) {
	if p.DefaultBranch == "" {
		repo, resp, err := p.GhClientPair.v3Client.Repositories.Get(p.Ctx, p.Owner, p.Repo)
		if err != nil {
			p.PrLogger.Errorf("Could not get repo default branch: err=%s\n%v\n", err, resp)
			return "", err
		}
		prom.InstrumentGhCall(resp)
		p.DefaultBranch = *repo.DefaultBranch
		return *repo.DefaultBranch, err
	} else {
		return p.DefaultBranch, nil
	}
}

func generateDeletionTreeEntries(ghPrClientDetails *GhPrClientDetails, path *string, branch *string, treeEntries *[]*github.TreeEntry) error {
	// GH tree API doesn't allow deletion a whole dir, so this recursive function traverse the whole tree
	// and create a tree entry array that would delete all the files in that path
	getContentOpts := &github.RepositoryContentGetOptions{
		Ref: *branch,
	}
	_, directoryContent, resp, err := ghPrClientDetails.GhClientPair.v3Client.Repositories.GetContents(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, *path, getContentOpts)
	prom.InstrumentGhCall(resp)
	if resp.StatusCode == 404 {
		ghPrClientDetails.PrLogger.Infof("Skipping deletion of non-existing  %s", *path)
		return nil
	} else if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Could not fetch %s content  err=%s\n%v\n", *path, err, resp)
		return err
	}
	for _, elementInDir := range directoryContent {
		if *elementInDir.Type == "file" {
			treeEntry := github.TreeEntry{ // https://docs.github.com/en/rest/git/trees?apiVersion=2022-11-28#create-a-tree
				Path:    github.String(*elementInDir.Path),
				Mode:    github.String("100644"),
				Type:    github.String("blob"),
				SHA:     nil,
				Content: nil,
			}
			*treeEntries = append(*treeEntries, &treeEntry)
		} else if *elementInDir.Type == "dir" {
			err := generateDeletionTreeEntries(ghPrClientDetails, elementInDir.Path, branch, treeEntries)
			if err != nil {
				return err
			}
		} else {
			ghPrClientDetails.PrLogger.Infof("Ignoring type %s for path %s", *elementInDir.Type, *elementInDir.Path)
		}
	}
	return nil
}

func generateBumpTreeEntiesForCommit(treeEntries *[]*github.TreeEntry, ghPrClientDetails GhPrClientDetails, defaultBranch string, filePath string, fileContent string) {
	treeEntry := github.TreeEntry{
		Path:    github.String(filePath),
		Mode:    github.String("100644"),
		Type:    github.String("blob"),
		Content: github.String(fileContent),
	}
	*treeEntries = append(*treeEntries, &treeEntry)
}

func getDirecotyGitObjectSha(ghPrClientDetails GhPrClientDetails, dirPath string, branch string) (string, error) {
	repoContentGetOptions := github.RepositoryContentGetOptions{
		Ref: branch,
	}

	direcotyGitObjectSha := ""
	// in GH API/go-github, to get directory SHA you need to scan the whole parent Dir 🤷
	_, directoryContent, resp, err := ghPrClientDetails.GhClientPair.v3Client.Repositories.GetContents(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, path.Dir(dirPath), &repoContentGetOptions)
	prom.InstrumentGhCall(resp)
	if err != nil && resp.StatusCode != 404 {
		ghPrClientDetails.PrLogger.Errorf("Could not fetch source directory SHA err=%s\n%v\n", err, resp)
		return "", err
	} else if err == nil { // scaning the parent dir
		for _, dirElement := range directoryContent {
			if *dirElement.Path == dirPath {
				direcotyGitObjectSha = *dirElement.SHA
				break
			}
		}
	} // leaving out statusCode 404, this means the whole parent dir is missing, but the behavior is similar to the case we didn't find the dir

	return direcotyGitObjectSha, nil
}

func GenerateSyncTreeEntriesForCommit(treeEntries *[]*github.TreeEntry, ghPrClientDetails GhPrClientDetails, sourcePath string, targetPath string, defaultBranch string) error {
	sourcePathSHA, err := getDirecotyGitObjectSha(ghPrClientDetails, sourcePath, defaultBranch)

	if sourcePathSHA == "" {
		ghPrClientDetails.PrLogger.Infoln("Source directory wasn't found, assuming a deletion PR")
		err := generateDeletionTreeEntries(&ghPrClientDetails, &targetPath, &defaultBranch, treeEntries)
		if err != nil {
			ghPrClientDetails.PrLogger.Errorf("Failed to build deletion tree: err=%s\n", err)
			return err
		}
	} else {
		syncTreeEntry := github.TreeEntry{
			Path: github.String(targetPath),
			Mode: github.String("040000"),
			Type: github.String("tree"),
			SHA:  github.String(sourcePathSHA),
		}
		*treeEntries = append(*treeEntries, &syncTreeEntry)

		// Aperntly... the way we sync directories(set the target dir git tree object SHA) doesn't delete files!!!! GH just "merges" the old and new tree objects.
		// So for now, I'll just go over all the files and add explicitly add  delete tree  entries  :(
		// TODO compare sourcePath targetPath Git object SHA to avoid costly tree compare where possible?
		sourceFilesSHAs := make(map[string]string)
		targetFilesSHAs := make(map[string]string)
		generateFlatMapfromFileTree(&ghPrClientDetails, &sourcePath, &sourcePath, &defaultBranch, sourceFilesSHAs)
		generateFlatMapfromFileTree(&ghPrClientDetails, &targetPath, &targetPath, &defaultBranch, targetFilesSHAs)

		for filename := range targetFilesSHAs {
			if _, found := sourceFilesSHAs[filename]; !found {
				ghPrClientDetails.PrLogger.Debugf("%s -- was NOT found on %s, marking as a deletion!", filename, sourcePath)
				fileDeleteTreeEntry := github.TreeEntry{
					Path:    github.String(targetPath + "/" + filename),
					Mode:    github.String("100644"),
					Type:    github.String("blob"),
					SHA:     nil, // this is how you delete a file https://docs.github.com/en/rest/git/trees?apiVersion=2022-11-28#create-a-tree
					Content: nil,
				}
				*treeEntries = append(*treeEntries, &fileDeleteTreeEntry)
			}
		}
	}

	return err
}

func createCommit(ghPrClientDetails GhPrClientDetails, treeEntries []*github.TreeEntry, defaultBranch string, commitMsg string) (*github.Commit, error) {
	// To avoid cloning the repo locally, I'm using GitHub low level GIT Tree API to sync the source folder "over" the target folders
	// This works by getting the source dir git object SHA, and overwriting(Git.CreateTree) the target directory git object SHA with the source's SHA.

	ref, resp, err := ghPrClientDetails.GhClientPair.v3Client.Git.GetRef(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, "heads/"+defaultBranch)
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Failed to get main branch ref: err=%s\n", err)
		return nil, err
	}
	baseTreeSHA := ref.Object.SHA
	tree, resp, err := ghPrClientDetails.GhClientPair.v3Client.Git.CreateTree(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, *baseTreeSHA, treeEntries)
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Failed to create Git Tree object: err=%s\n%+v", err, resp)
		ghPrClientDetails.PrLogger.Errorf("These are the treeEntries: %+v", treeEntries)
		return nil, err
	}
	parentCommit, resp, err := ghPrClientDetails.GhClientPair.v3Client.Git.GetCommit(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, *baseTreeSHA)
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Failed to get parent commit: err=%s\n", err)
		return nil, err
	}

	newCommitConfig := &github.Commit{
		Message: github.String(commitMsg),
		Parents: []*github.Commit{parentCommit},
		Tree:    tree,
	}

	commit, resp, err := ghPrClientDetails.GhClientPair.v3Client.Git.CreateCommit(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, newCommitConfig, nil)
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Failed to create Git commit: err=%s\n", err) // TODO comment this error to PR
		return nil, err
	}

	return commit, err
}

func createBranch(ghPrClientDetails GhPrClientDetails, commit *github.Commit, newBranchName string) (string, error) {
	newBranchRef := "refs/heads/" + newBranchName
	ghPrClientDetails.PrLogger.Infof("New branch name will be: %s", newBranchName)

	newRefGitObjct := &github.GitObject{
		SHA: commit.SHA,
	}

	newRefConfig := &github.Reference{
		Ref:    github.String(newBranchRef),
		Object: newRefGitObjct,
	}

	_, resp, err := ghPrClientDetails.GhClientPair.v3Client.Git.CreateRef(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, newRefConfig)
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Could not create Git Ref: err=%s\n%v\n", err, resp)
		return "", err
	}
	ghPrClientDetails.PrLogger.Infof("New branch ref: %s", newBranchRef)
	return newBranchRef, err
}

func generatePromotionPrBody(ghPrClientDetails GhPrClientDetails, components string, promotion PromotionInstance, originalPrAuthor string) string {
	// newPrMetadata will be serialized and persisted in the PR body for use when the PR is merged
	var newPrMetadata prMetadata
	var newPrBody string

	newPrMetadata.OriginalPrAuthor = originalPrAuthor

	if ghPrClientDetails.PrMetadata.PreviousPromotionMetadata != nil {
		newPrMetadata.PreviousPromotionMetadata = ghPrClientDetails.PrMetadata.PreviousPromotionMetadata
	} else {
		newPrMetadata.PreviousPromotionMetadata = make(map[int]promotionInstanceMetaData)
	}

	newPrMetadata.PreviousPromotionMetadata[ghPrClientDetails.PrNumber] = promotionInstanceMetaData{
		TargetPaths: promotion.Metadata.TargetPaths,
		SourcePath:  promotion.Metadata.SourcePath,
	}
	// newPrMetadata.PreviousPromotionMetadata[ghPrClientDetails.PrNumber].TargetPaths = targetPaths
	// newPrMetadata.PreviousPromotionMetadata[ghPrClientDetails.PrNumber].SourcePath = sourcePath

	newPrMetadata.PromotedPaths = maps.Keys(promotion.ComputedSyncPaths)

	promotionSkipPaths := getPromotionSkipPaths(promotion)

	newPrBody = fmt.Sprintf("Promotion path(%s):\n\n", components)

	keys := make([]int, 0)
	for k := range newPrMetadata.PreviousPromotionMetadata {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	newPrBody = prBody(keys, newPrMetadata, newPrBody, promotionSkipPaths)

	prMetadataString, _ := newPrMetadata.serialize()

	newPrBody = newPrBody + "\n<!--|Telefonistka data, do not delete|" + prMetadataString + "|-->"

	return newPrBody
}

// getPromotionSkipPaths returns a map of paths that are marked as skipped for this promotion
// when we have multiple components, we are going to use the component that has the fewest skip paths
func getPromotionSkipPaths(promotion PromotionInstance) map[string]bool {
	perComponentSkippedTargetPaths := promotion.Metadata.PerComponentSkippedTargetPaths
	promotionSkipPaths := map[string]bool{}

	if len(perComponentSkippedTargetPaths) == 0 {
		return promotionSkipPaths
	}

	// if any promoted component is not in the perComponentSkippedTargetPaths
	// then that means we have a component that is promoted to all paths,
	// therefore, we return an empty promotionSkipPaths map to signify that
	// there are no paths that are skipped for this promotion
	for _, component := range promotion.Metadata.ComponentNames {
		if _, ok := perComponentSkippedTargetPaths[component]; !ok {
			return promotionSkipPaths
		}
	}

	// if we have one or more components then we are just going to
	// user the component that has the fewest skipPaths when
	// generating the promotion prBody. This way the promotion
	// body will error on the side of informing the user
	// of more promotion paths, rather than leaving some out.
	skipCounts := map[string]int{}
	for component, paths := range perComponentSkippedTargetPaths {
		skipCounts[component] = len(paths)
	}

	skipPaths := maps.Keys(skipCounts)
	slices.SortFunc(skipPaths, func(a, b string) int {
		return cmp.Compare(skipCounts[a], skipCounts[b])
	})

	componentWithFewestSkippedPaths := skipPaths[0]
	for _, p := range perComponentSkippedTargetPaths[componentWithFewestSkippedPaths] {
		promotionSkipPaths[p] = true
	}

	return promotionSkipPaths
}

func prBody(keys []int, newPrMetadata prMetadata, newPrBody string, promotionSkipPaths map[string]bool) string {
	const mkTab = "&nbsp;&nbsp;&nbsp;&nbsp;"
	sp := ""
	tp := ""

	for i, k := range keys {
		sp = newPrMetadata.PreviousPromotionMetadata[k].SourcePath
		x := filterSkipPaths(newPrMetadata.PreviousPromotionMetadata[k].TargetPaths, promotionSkipPaths)
		// sort the paths so that we have a predictable order for tests and better readability for users
		sort.Strings(x)
		tp = strings.Join(x, fmt.Sprintf("`  \n%s`", strings.Repeat(mkTab, i+1)))
		newPrBody = newPrBody + fmt.Sprintf("%s↘️  #%d  `%s` ➡️  \n%s`%s`  \n", strings.Repeat(mkTab, i), k, sp, strings.Repeat(mkTab, i+1), tp)
	}

	return newPrBody
}

// filterSkipPaths filters out the paths that are marked as skipped
func filterSkipPaths(targetPaths []string, promotionSkipPaths map[string]bool) []string {
	pathSkip := make(map[string]bool)
	for _, targetPath := range targetPaths {
		if _, ok := promotionSkipPaths[targetPath]; ok {
			pathSkip[targetPath] = true
		} else {
			pathSkip[targetPath] = false
		}
	}

	var paths []string

	for path, skip := range pathSkip {
		if !skip {
			paths = append(paths, path)
		}
	}

	return paths
}

func createPrObject(ghPrClientDetails GhPrClientDetails, newBranchRef string, newPrTitle string, newPrBody string, defaultBranch string, assignee string) (*github.PullRequest, error) {
	newPrConfig := &github.NewPullRequest{
		Body:  github.String(newPrBody),
		Title: github.String(newPrTitle),
		Base:  github.String(defaultBranch),
		Head:  github.String(newBranchRef),
	}

	pull, resp, err := ghPrClientDetails.GhClientPair.v3Client.PullRequests.Create(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, newPrConfig)
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Could not create GitHub PR: err=%s\n%v\n", err, resp)
		return nil, err
	} else {
		ghPrClientDetails.PrLogger.Infof("PR %d opened", *pull.Number)
	}

	prLables, resp, err := ghPrClientDetails.GhClientPair.v3Client.Issues.AddLabelsToIssue(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, *pull.Number, []string{"promotion"})
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Could not label GitHub PR: err=%s\n%v\n", err, resp)
		return pull, err
	} else {
		ghPrClientDetails.PrLogger.Debugf("PR %v labeled\n%+v", pull.Number, prLables)
	}

	_, resp, err = ghPrClientDetails.GhClientPair.v3Client.Issues.AddAssignees(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, *pull.Number, []string{assignee})
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Warnf("Could not set %s as assignee on PR,  err=%s", assignee, err)
		// return pull, err
	} else {
		ghPrClientDetails.PrLogger.Debugf(" %s was set as assignee on PR", assignee)
	}

	return pull, nil // TODO
}

func ApprovePr(approverClient *github.Client, ghPrClientDetails GhPrClientDetails, prNumber *int) error {
	reviewRequest := &github.PullRequestReviewRequest{
		Event: github.String("APPROVE"),
	}

	_, resp, err := approverClient.PullRequests.CreateReview(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, *prNumber, reviewRequest)
	prom.InstrumentGhCall(resp)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Could not create review: err=%s\n%v\n", err, resp)
		return err
	}

	return nil
}

func GetInRepoConfig(ghPrClientDetails GhPrClientDetails, defaultBranch string) (*cfg.Config, error) {
	inRepoConfigFileContentString, _, err := GetFileContent(ghPrClientDetails, defaultBranch, "telefonistka.yaml")
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Could not get in-repo configuration: err=%s\n", err)
		inRepoConfigFileContentString = ""
	}
	c, err := cfg.ParseConfigFromYaml(inRepoConfigFileContentString)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Failed to parse configuration: err=%s\n", err)
	}
	return c, err
}

func GetFileContent(ghPrClientDetails GhPrClientDetails, branch string, filePath string) (string, int, error) {
	rGetContentOps := github.RepositoryContentGetOptions{Ref: branch}
	fileContent, _, resp, err := ghPrClientDetails.GhClientPair.v3Client.Repositories.GetContents(ghPrClientDetails.Ctx, ghPrClientDetails.Owner, ghPrClientDetails.Repo, filePath, &rGetContentOps)
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Fail to get file:%s\n%v\n", err, resp)
		if resp == nil {
			return "", 0, err
		}
		prom.InstrumentGhCall(resp)
		return "", resp.StatusCode, err
	} else {
		prom.InstrumentGhCall(resp)
	}
	fileContentString, err := fileContent.GetContent()
	if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Fail to serlize file:%s\n", err)
		return "", resp.StatusCode, err
	}
	return fileContentString, resp.StatusCode, nil
}

// commitStatusTargetURL generates a target URL based on an optional
// template file specified by the environment variable CUSTOM_COMMIT_STATUS_URL_TEMPLATE_PATH.
// If the template file is not found or an error occurs during template execution,
// it returns a default URL.
// passed parameter commitTime can be used in the template as .CommitTime
func commitStatusTargetURL(commitTime time.Time, tmplFile string) string {
	const targetURL string = "https://github.com/wayfair-incubator/telefonistka"

	tmplName := filepath.Base(tmplFile)

	// dynamic parameters to be used in the template
	p := struct {
		CommitTime time.Time
	}{
		CommitTime: commitTime,
	}
	renderedURL, err := executeTemplate(tmplName, tmplFile, p)
	if err != nil {
		log.Debugf("Failed to render target URL template: %v", err)
		return targetURL
	}

	// trim any leading/trailing whitespace
	renderedURL = strings.TrimSpace(renderedURL)
	return renderedURL
}
