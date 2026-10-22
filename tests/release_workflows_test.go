package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowStep struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	Uses            string            `yaml:"uses"`
	If              string            `yaml:"if"`
	Run             string            `yaml:"run"`
	With            map[string]string `yaml:"with"`
	Env             map[string]string `yaml:"env"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

type workflowJob struct {
	Needs           []string          `yaml:"needs"`
	If              string            `yaml:"if"`
	Uses            string            `yaml:"uses"`
	With            map[string]string `yaml:"with"`
	Outputs         map[string]string `yaml:"outputs"`
	Permissions     map[string]string `yaml:"permissions"`
	Secrets         string            `yaml:"secrets"`
	Steps           []workflowStep    `yaml:"steps"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

type workflowDocument struct {
	On          yaml.Node              `yaml:"on"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
	Concurrency struct {
		Group string `yaml:"group"`
	} `yaml:"concurrency"`
}

func readWorkflow(t *testing.T, name string) workflowDocument {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowDocument
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("解析 %s: %v", name, err)
	}
	return workflow
}

func containsNeed(needs []string, want string) bool {
	for _, need := range needs {
		if need == want {
			return true
		}
	}
	return false
}

func checkoutStep(t *testing.T, steps []workflowStep) workflowStep {
	t.Helper()
	for _, step := range steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			return step
		}
	}
	t.Fatal("缺少源码 checkout")
	return workflowStep{}
}

func TestReleaseWorkflowsGateEveryPublishedCommit(t *testing.T) {
	for file, publishJobs := range map[string][]string{"release.yml": {"build", "docker"}} {
		t.Run(file, func(t *testing.T) {
			workflow := readWorkflow(t, file)
			resolve, ok := workflow.Jobs["resolve"]
			if !ok {
				t.Fatal("发布入口必须先解析目标为固定提交 SHA")
			}
			if resolve.Permissions["contents"] != "read" || resolve.Outputs["commit_sha"] != "${{ steps.commit.outputs.sha }}" {
				t.Fatal("提交解析必须只读并输出实际 checkout 的 SHA")
			}
			if got := checkoutStep(t, resolve.Steps).With["ref"]; got != "${{ inputs.commit_sha || github.sha }}" {
				t.Fatalf("提交解析未保留自定义 ref 和缺省事件 SHA: %q", got)
			}
			gate, ok := workflow.Jobs["quality"]
			if !ok || !containsNeed(gate.Needs, "resolve") || gate.Uses != "./.github/workflows/quality-checks.yml" || gate.With["commit_sha"] != "${{ needs.resolve.outputs.commit_sha }}" {
				t.Fatal("完整质量检查必须使用解析后的同一提交")
			}
			if gate.ContinueOnError || gate.If != "" || gate.Permissions["contents"] != "read" {
				t.Fatal("质量门禁不得绕过失败或拥有发布权限")
			}
			for _, publishJob := range publishJobs {
				publish := workflow.Jobs[publishJob]
				if !containsNeed(publish.Needs, "resolve") || !containsNeed(publish.Needs, "quality") || publish.ContinueOnError {
					t.Fatal("发布 job 必须依赖解析和完整质量检查成功，不能忽略失败")
				}
				selector := "build_package"
				if publishJob == "docker" {
					selector = "build_image"
				}
				if publish.If != "(github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')) || inputs."+selector {
					t.Fatal("发布只能按产物选择开关过滤，不得用 always 或成功状态表达式绕过门禁")
				}
				if containsNeed(publish.Needs, "build") || containsNeed(publish.Needs, "docker") {
					t.Fatal("包和镜像应在质量检查后并行构建")
				}
				if got := checkoutStep(t, publish.Steps).With["ref"]; got != "${{ needs.resolve.outputs.commit_sha }}" {
					t.Fatalf("发布重新解析了可移动 ref，未使用已验证 SHA: %q", got)
				}
			}
			for _, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if strings.Contains(step.Run, "${{") {
						t.Fatalf("%s 直接向 shell 注入表达式；应经 env 传入", step.Name)
					}
				}
			}
		})
	}
}

func TestManualReleaseEntrypointsKeepTheSharedQualityGate(t *testing.T) {
	for file, selected := range map[string]string{"build.yml": "build_package", "build-image.yml": "build_image"} {
		t.Run(file, func(t *testing.T) {
			workflow := readWorkflow(t, file)
			var events map[string]yaml.Node
			if err := workflow.On.Decode(&events); err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events["workflow_dispatch"].Kind == 0 {
				t.Fatal("旧入口只保留手动触发，标签发布由统一流水线负责")
			}
			if len(workflow.Jobs) != 1 {
				t.Fatal("手动入口应委托统一流水线，不另建可绕过的发布 job")
			}
			call := workflow.Jobs["release"]
			if call.Uses != "./.github/workflows/release.yml" || call.Secrets != "inherit" || call.If != "" || call.ContinueOnError {
				t.Fatal("手动入口必须完整委托受质量门禁保护的流水线")
			}
			if call.With["commit_sha"] != "${{ inputs.commit_sha }}" || call.With["version"] != "${{ inputs.version }}" || call.With[selected] != "true" {
				t.Fatal("手动选择的 ref、版本和产物必须完整传递")
			}
			other := "build_image"
			if selected == other {
				other = "build_package"
			}
			if call.With[other] != "false" {
				t.Fatal("独立入口只发布所选产物")
			}
		})
	}
}

func TestPrereleaseImagesDoNotReplaceLatest(t *testing.T) {
	workflow := readWorkflow(t, "release.yml")
	latestRules := 0
	for _, step := range workflow.Jobs["docker"].Steps {
		if !strings.HasPrefix(step.Uses, "docker/metadata-action@") {
			continue
		}
		for _, rule := range strings.Split(step.With["tags"], "\n") {
			if !strings.Contains(rule, "value=latest") {
				continue
			}
			latestRules++
			if !strings.Contains(rule, "enable=${{ inputs.release_type != '预发布版（pre-release）' && (") {
				t.Fatal("预发布镜像不得覆盖 latest；所有事件条件都必须受发布类型保护")
			}
		}
	}
	if latestRules != 1 {
		t.Fatal("必须只有一条受保护的 latest 镜像标签规则")
	}
}

func TestReusableQualityChecksUseRequestedCommitAndKeepAllGates(t *testing.T) {
	workflow := readWorkflow(t, "quality-checks.yml")
	var events struct {
		WorkflowCall struct {
			Inputs map[string]struct {
				Required bool   `yaml:"required"`
				Type     string `yaml:"type"`
			} `yaml:"inputs"`
		} `yaml:"workflow_call"`
	}
	if err := workflow.On.Decode(&events); err != nil {
		t.Fatal(err)
	}
	input := events.WorkflowCall.Inputs["commit_sha"]
	if !input.Required || input.Type != "string" {
		t.Fatal("复用质量检查必须要求固定 commit_sha")
	}
	var commands strings.Builder
	for _, name := range []string{"test", "docker"} {
		job := workflow.Jobs[name]
		if job.If != "" || job.ContinueOnError {
			t.Fatalf("质量检查 %s 不得跳过或忽略失败", name)
		}
		if got := checkoutStep(t, job.Steps).With["ref"]; got != "${{ inputs.commit_sha || github.sha }}" {
			t.Fatalf("%s 未检出请求的提交: %q", name, got)
		}
		verified := false
		for _, step := range job.Steps {
			if step.ContinueOnError {
				t.Fatalf("%s 不得忽略检查失败", step.Name)
			}
			if step.Env["EXPECTED_COMMIT"] == "${{ inputs.commit_sha }}" && strings.Contains(step.Run, "git rev-parse HEAD") && strings.Contains(step.Run, "$EXPECTED_COMMIT") {
				verified = true
			}
			commands.WriteString(step.Run)
			commands.WriteByte('\n')
		}
		if !verified {
			t.Fatalf("%s 必须核验检出 SHA", name)
		}
	}
	for _, required := range []string{"node --check", "node --test", "python3 -m unittest discover -s tests -p test_ncmm_update_script.py", "python3 -m unittest discover -s tests -p test_release_guard.py", "go vet ./...", "go test ./...", "go test -race", "./internal/selfupdate", "./internal/filelock", "./internal/atomicfile", "tests/webui_smoke.js", "tests/webui_music_partner.js", "tests/webui_cookie_status.js", "tests/webui_task_config.js", "tests/webui_mobile_config.js", "docker compose config --quiet", "docker build", "--network none"} {
		if !strings.Contains(commands.String(), required) {
			t.Errorf("发布质量门禁缺少既有检查: %s", required)
		}
	}
}

func TestReleaseGuardPrecedesEveryPublicationAndPreservesAssets(t *testing.T) {
	workflow := readWorkflow(t, "release.yml")
	if workflow.Concurrency.Group != "ncmm-release-${{ github.repository }}" {
		t.Fatal("所有版本输入必须共用仓库级发布队列，避免同版本同时发布")
	}
	if workflow.Jobs["resolve"].Outputs["version"] != "${{ steps.release.outputs.version }}" {
		t.Fatal("发布版本必须随不可变提交只解析一次")
	}
	for _, name := range []string{"resolve", "build", "docker"} {
		job := workflow.Jobs[name]
		guarded := false
		reserved := false
		publications := 0
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "release_guard.py") {
				guarded = !step.ContinueOnError && strings.Contains(step.Run, "--source-sha") && strings.Contains(step.Run, "--version")
				reserved = guarded && strings.Contains(step.Run, "--reserve-tag") && step.Env["GH_TOKEN"] == "${{ secrets.GITHUB_TOKEN }}"
			}
			if strings.Contains(step.Run, "git tag -d") || strings.Contains(step.Run, "git push --force") {
				t.Fatal("发布不得删除或覆盖既有标签")
			}
			isRelease := strings.HasPrefix(step.Uses, "softprops/action-gh-release@")
			isImage := strings.HasPrefix(step.Uses, "docker/build-push-action@") && step.With["push"] == "true"
			if isRelease || isImage {
				publications++
				if !guarded || step.ContinueOnError {
					t.Fatalf("%s 未在发布前拒绝远端标签冲突", name)
				}
				if !reserved || job.Permissions["contents"] != "write" {
					t.Fatalf("%s 必须在包或镜像发布前原子预留同 SHA 的远端版本标签", name)
				}
				if isRelease && step.With["overwrite_files"] != "false" {
					t.Fatal("既有 Release 资产不得被同名重跑覆盖")
				}
				guarded = false
			}
		}
		if name == "resolve" && !guarded {
			t.Fatal("必须在构建及质量检查前验证标签身份")
		}
		if name != "resolve" && publications != 1 {
			t.Fatalf("%s 预期一个受保护发布步骤", name)
		}
	}
}

func TestReusableReleasePackageStepsRespectSelectedProducts(t *testing.T) {
	workflow := readWorkflow(t, "release.yml")
	for _, step := range workflow.Jobs["build"].Steps {
		if strings.HasPrefix(step.Uses, "goreleaser/goreleaser-action@") && step.Env["GORELEASER_CURRENT_TAG"] != "v${{ env.VERSION }}" {
			t.Fatal("同一提交存在多个标签时，程序包版本必须仍使用已验证的目标版本")
		}
		if step.Name == "Create local Git Tag for GoReleaser" || step.Name == "Generate Custom Changelog" || strings.HasPrefix(step.Uses, "softprops/action-gh-release@") {
			// workflow_call 沿用 caller 的事件，不能再按 dispatch/tag 限制已选中的包发布。
			if step.If != "" {
				t.Fatal("已选中的包发布不得重新按 caller 的事件类型过滤")
			}
		}
	}
}

func TestVIPGiftCloudPublicationUsesItsOwnValidatedCommit(t *testing.T) {
	workflow := readWorkflow(t, "build-vipgift-image.yml")
	resolve := workflow.Jobs["resolve"]
	if resolve.Outputs["commit_sha"] != "${{ steps.commit.outputs.sha }}" || resolve.Permissions["contents"] != "read" {
		t.Fatal("辅助服务必须先只读解析发布提交")
	}
	gate := workflow.Jobs["quality"]
	if gate.Uses != "./.github/workflows/quality-vipgift.yml" || !containsNeed(gate.Needs, "resolve") || gate.With["commit_sha"] != "${{ needs.resolve.outputs.commit_sha }}" || gate.If != "" || gate.ContinueOnError {
		t.Fatal("辅助发布必须通过独立实际质量检查，不能重复运行主程序门禁或忽略失败")
	}
	publish := workflow.Jobs["docker-cloud"]
	if !containsNeed(publish.Needs, "quality") || !containsNeed(publish.Needs, "resolve") || publish.ContinueOnError {
		t.Fatal("辅助镜像发布必须依赖固定提交的完整辅助服务检查")
	}
	if checkoutStep(t, publish.Steps).With["ref"] != "${{ needs.resolve.outputs.commit_sha }}" {
		t.Fatal("辅助服务不得在门禁之后重新解析移动分支")
	}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "${{") {
				t.Fatal("辅助服务的手动输入必须通过环境变量传入脚本")
			}
		}
	}
}

func TestVIPGiftCloudQualityActuallyExercisesAPIsAndBuildsWithoutPublishing(t *testing.T) {
	workflow := readWorkflow(t, "quality-vipgift.yml")
	var events map[string]yaml.Node
	if err := workflow.On.Decode(&events); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"push", "pull_request", "workflow_call"} {
		if events[event].Kind == 0 {
			t.Errorf("辅助质量检查缺少 %s 入口", event)
		}
	}
	var commands strings.Builder
	for _, job := range workflow.Jobs {
		if job.If != "" || job.ContinueOnError {
			t.Fatal("辅助质量检查不得跳过或忽略失败")
		}
		if checkoutStep(t, job.Steps).With["ref"] != "${{ inputs.commit_sha || github.sha }}" {
			t.Fatal("辅助质量检查未使用请求的固定提交")
		}
		for _, step := range job.Steps {
			if step.ContinueOnError || step.With["push"] == "true" || strings.HasPrefix(step.Uses, "docker/login-action@") {
				t.Fatal("辅助质量检查不得发布镜像、登录或忽略失败")
			}
			commands.WriteString(step.Run)
			commands.WriteByte('\n')
		}
	}
	for _, required := range []string{"git rev-parse HEAD", "python3 -m py_compile", "python3 -m unittest discover -s tests -p test_vip_gift_cloud.py", "docker compose", "config --quiet", "docker build", "--network none"} {
		if !strings.Contains(commands.String(), required) {
			t.Errorf("辅助质量检查缺少实际验证: %s", required)
		}
	}
}

func TestVIPGiftComposeRunsTheDocumentedLocalBuild(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "tools", "vip_member_gift_cloud", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Image      string `yaml:"image"`
			PullPolicy string `yaml:"pull_policy"`
			Build      struct {
				Context string `yaml:"context"`
			} `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatal(err)
	}
	service := compose.Services["ncmm-vip-member-gift-cloud"]
	if service.Image != "ncmm-vip-member-gift-cloud:local" || service.PullPolicy != "never" || service.Build.Context != "." {
		t.Fatal("Compose 必须直接构建和运行文档中的本地镜像，不得误拉上游替代")
	}
}
